package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
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
	// refusing is true between the first refused produce and the next
	// accepted one, so the outage is logged once rather than per event.
	refusing atomic.Bool
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
		refusing:       atomic.Bool{},
	}, nil
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
		// A refusal is logged once per outage rather than once per event.
		// The first after a success logs at Warn with the broker's error;
		// every refusal until the next success logs at Debug. A broker down
		// for an hour therefore costs one Warn and one Info line rather
		// than one Error per write, which was the flood TACK-320 names.
		// What became of the refused event is the caller's to say: behind
		// a SpillRecorder it is durable, and a caller with nowhere to put
		// it logs its own loss.
		if k.refusing.CompareAndSwap(false, true) {
			slog.WarnContext(ctx, "kafka.produce.refusing",
				slog.String("err", produceErr.Error()),
				slog.String("topic", k.topic),
				slog.String("event_id", eventID),
				slog.String("verb", ev.Verb),
			)
		} else {
			slog.DebugContext(ctx, "kafka.produce.failed",
				slog.String("err", produceErr.Error()),
				slog.String("topic", k.topic),
				slog.String("event_id", eventID),
				slog.String("verb", ev.Verb),
			)
		}
		return fmt.Errorf("audit kafka produce verb=%s: %w", ev.Verb, produceErr)
	}
	telemetry.IncAuditKafkaProduce("ok")
	if k.refusing.CompareAndSwap(true, false) {
		slog.InfoContext(ctx, "kafka.produce.recovered", slog.String("topic", k.topic))
	}
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
