package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/telemetry"
)

// KafkaRecorder produces audit events to an Apache Kafka topic. It is the
// producer-side piece of the audit cutover: it ships the JSON Event payload
// that the audit-consumer projects into the canonical audit.events row.
//
// Produce is synchronous with acks=all and idempotence enabled. Errors
// surface to the caller; this recorder never silently drops an event.
type KafkaRecorder struct {
	client         *kgo.Client
	topic          string
	produceTimeout time.Duration
}

// KafkaConfig collects the producer knobs that vary between deployments.
// Brokers and Topic are required; the other fields fall back to
// production-friendly defaults when zero.
type KafkaConfig struct {
	// Brokers is the comma-separated bootstrap broker list. The franz-go
	// client uses bootstrap brokers to discover the rest of the cluster
	// at runtime, so this list is the same shape at N=1 and N=many.
	Brokers []string

	// Topic is the Kafka topic name. The Phase 2 design fixes the v1
	// topic at audit.events.v1; the producer treats the topic as opaque.
	Topic string

	// ClientID labels the producer in broker-side metrics and logs.
	ClientID string

	// ProduceTimeout caps a single Record call. Nonpositive values fall back to 15s.
	ProduceTimeout time.Duration
}

func applyKafkaDefaults(cfg KafkaConfig) KafkaConfig {
	if cfg.ProduceTimeout <= 0 {
		// 15s: this repo does not override Kafka's external
		// broker.session.timeout.ms default of 9s (broker lease) or franz-go's
		// external 5s metadata minimum age. A hard broker loss can consume both
		// before recovery; 10s expired inside that window and surfaced spurious
		// Record errors during single-broker failures.
		cfg.ProduceTimeout = 15 * time.Second
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "tack-audit-producer"
	}
	return cfg
}

// NewKafkaRecorder constructs a KafkaRecorder. The franz-go client opens
// connections lazily, so this returns immediately even when the broker
// is unreachable; the first Record call surfaces any connection error.
func NewKafkaRecorder(cfg KafkaConfig) (*KafkaRecorder, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("audit kafka: at least one broker required")
	}
	if cfg.Topic == "" {
		return nil, errors.New("audit kafka: topic required")
	}
	cfg = applyKafkaDefaults(cfg)
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.DefaultProduceTopic(cfg.Topic),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.Lz4Compression()),
		kgo.ProducerLinger(5 * time.Millisecond),
		kgo.ProduceRequestTimeout(cfg.ProduceTimeout),
		kgo.RecordDeliveryTimeout(cfg.ProduceTimeout),
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		slog.Error("audit.kafka.client_init_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit kafka: new client: %w", err)
	}
	return &KafkaRecorder{
		client:         client,
		topic:          cfg.Topic,
		produceTimeout: cfg.ProduceTimeout,
	}, nil
}

// Ready reports whether this producer can publish its ledger. The client
// connects lazily, so construction alone proves nothing; startup calls this so
// a server that cannot record refuses to serve instead of running unrecorded
// until someone reads a log.
//
// A broker answering is not enough. The topic has to exist, be visible to this
// client, and have a leader on every partition, because a reachable broker
// whose topic is missing or stranded fails Record while looking healthy at the
// connection level.
func (k *KafkaRecorder) Ready(ctx context.Context) error {
	if err := k.client.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.kafka.ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit kafka ping: %w", err)
	}
	return k.checkTopicWritable(ctx)
}

// checkTopicWritable asks the cluster about the configured topic and demands a
// leader on every partition. Auto-creation is disabled on the request: a topic
// conjured by the readiness check would report success while the partition
// count the design fixes had never been applied.
func (k *KafkaRecorder) checkTopicWritable(ctx context.Context) error {
	req := kmsg.NewPtrMetadataRequest()
	reqTopic := kmsg.NewMetadataRequestTopic()
	reqTopic.Topic = &k.topic
	req.Topics = append(req.Topics, reqTopic)
	req.AllowAutoTopicCreation = false

	resp, err := req.RequestWith(ctx, k.client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka.topic_metadata_failed",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return fmt.Errorf("audit kafka topic %s metadata: %w", k.topic, err)
	}
	if len(resp.Topics) == 0 {
		err := fmt.Errorf("audit kafka topic %s: cluster returned no metadata for it", k.topic)
		slog.ErrorContext(ctx, "audit.kafka.topic_absent",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return err
	}
	for _, respTopic := range resp.Topics {
		if respTopic.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
			slog.ErrorContext(ctx, "audit.kafka.topic_unwritable",
				slog.String("topic", k.topic), slog.String("err", codeErr.Error()))
			return fmt.Errorf("audit kafka topic %s: %w", k.topic, codeErr)
		}
		if err := k.checkPartitionLeaders(ctx, respTopic); err != nil {
			return err
		}
	}
	return nil
}

// checkPartitionLeaders demands a leader on every partition, not merely one.
// The producer key hashes an (org, shard) pair across the whole partition
// space, so a single leaderless partition fails every event that lands on it
// while the rest of the topic looks healthy. A topic with no partitions at all
// can accept nothing.
func (k *KafkaRecorder) checkPartitionLeaders(ctx context.Context, respTopic kmsg.MetadataResponseTopic) error {
	if len(respTopic.Partitions) == 0 {
		err := fmt.Errorf("audit kafka topic %s: reports no partitions", k.topic)
		slog.ErrorContext(ctx, "audit.kafka.topic_partitionless",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return err
	}
	for _, partition := range respTopic.Partitions {
		if partition.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(partition.ErrorCode)
			slog.ErrorContext(ctx, "audit.kafka.partition_unwritable",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", codeErr.Error()))
			return fmt.Errorf("audit kafka topic %s partition %d: %w",
				k.topic, partition.Partition, codeErr)
		}
		if partition.Leader < 0 {
			err := fmt.Errorf("audit kafka topic %s partition %d: no leader",
				k.topic, partition.Partition)
			slog.ErrorContext(ctx, "audit.kafka.partition_leaderless",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", err.Error()))
			return err
		}
	}
	return nil
}

// MarshalEvent encodes an Event as canonical JSON. Exposed as an
// encoder-shaped helper so the wrapped error from [encoding/json.Marshal]
// is a codec error, not a side-effecting recorder error. Used by Record
// and callable directly from tests.
func MarshalEvent(ev Event) ([]byte, error) {
	out, err := json.Marshal(ev)
	if err != nil {
		slog.Error("audit.kafka.marshal_failed", slog.String("verb", ev.Verb), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit kafka marshal: %w", err)
	}
	return out, nil
}

// Record marshals the event to JSON and produces it synchronously to the
// configured topic. Returns the broker error on failure; never swallows.
//
// EventID and OccurredAt are taken verbatim from ev. CanonicalRecorder
// stamps both at the recording call site, so the Kafka payload and the
// consumer's chain hash agree on one identity and one timestamp.
func (k *KafkaRecorder) Record(ctx context.Context, ev Event) error {
	payload, err := MarshalEvent(ev)
	if err != nil {
		telemetry.IncAuditDropped(ev.Verb, "kafka_marshal")
		telemetry.IncAuditKafkaProduce("error")
		return err
	}
	produceCtx, cancel := context.WithTimeout(ctx, k.produceTimeout)
	defer cancel()
	eventID := eventIDForLog(ev)
	rec := &kgo.Record{
		Topic: k.topic,
		Key:   kafkaPartitionKey(ev),
		Value: payload,
	}
	telemetry.IncAuditKafkaProduceInflight()
	start := monoStart()
	res := k.client.ProduceSync(produceCtx, rec)
	telemetry.DecAuditKafkaProduceInflight()
	telemetry.ObserveAuditKafkaProduceLatency(sinceMs(start))
	if produceErr := res.FirstErr(); produceErr != nil {
		telemetry.IncAuditKafkaProduce("error")
		slog.ErrorContext(ctx, "kafka.produce.failed",
			slog.String("err", produceErr.Error()),
			slog.String("topic", k.topic),
			slog.String("event_id", eventID),
			slog.Int("attempt", 1),
			slog.String("verb", ev.Verb),
		)
		return fmt.Errorf("audit kafka produce verb=%s: %w", ev.Verb, produceErr)
	}
	telemetry.IncAuditKafkaProduce("ok")
	telemetry.L(ctx).Debug("audit.kafka.produced",
		slog.String("verb", ev.Verb),
		slog.String("topic", k.topic),
		slog.Int("payload_bytes", len(payload)),
		slog.String("event_id", eventID),
	)
	return nil
}

// eventIDForLog returns a stable identifier for the produce-failed log line
// so an operator can correlate the failure with downstream signals. It
// prefers the canonical EventID. It falls back to the IdempotencyKey, then to
// the actor and entity IDs joined by a colon, which is never empty.
func eventIDForLog(ev Event) string {
	if ev.EventID != uuid.Nil {
		return ev.EventID.String()
	}
	if ev.IdempotencyKey != "" {
		return ev.IdempotencyKey
	}
	return ev.Actor.ID.String() + ":" + ev.Entity.ID.String()
}

// Close flushes the producer queue and shuts the underlying client down.
// Implements the parameterless Close shape the server shutdown path
// type-asserts against; uses [context.Background] internally for the flush deadline so
// shutdown progresses even when the caller's context has already been
// cancelled by SIGINT.
func (k *KafkaRecorder) Close() error {
	return k.CloseContext(context.Background())
}

// CloseContext flushes the producer queue and shuts the underlying client
// down using the supplied context for the flush deadline.
func (k *KafkaRecorder) CloseContext(ctx context.Context) error {
	if k == nil || k.client == nil {
		return nil
	}
	flushCtx, cancel := context.WithTimeout(ctx, k.produceTimeout)
	defer cancel()
	if err := k.client.Flush(flushCtx); err != nil {
		slog.ErrorContext(ctx, "audit.kafka.flush_failed", slog.String("err", err.Error()))
		k.client.Close()
		return fmt.Errorf("audit kafka flush: %w", err)
	}
	k.client.Close()
	return nil
}

// kafkaPartitionKey packs (org_id, shard) as the producer key. Every event on
// one (org, shard) chain then lands on the same partition. Kafka assigns one
// partition to one consumer in a group, so exactly one consumer ever advances
// a given chain. The shard comes from shardOf(actor_id, event_id), the same
// function the consumer uses, so the key and the chain unit always agree.
func kafkaPartitionKey(ev Event) []byte {
	shard := int(shardOf(ev.Actor.ID, ev.EventID))
	out := make([]byte, 0, 18)
	out = append(out, ev.Context.OrgID[:]...)
	out = append(out, byte((shard>>8)&0xFF), byte(shard&0xFF))
	return out
}

// SplitBrokers parses a comma-separated broker list, trimming whitespace
// and discarding empty entries. Exposed for the cmd/server wiring.
func SplitBrokers(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
