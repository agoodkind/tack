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

// KafkaRecorder synchronously publishes JSON events to Kafka with acks=all
// and idempotence enabled. The consumer projects them into audit.events.
// Produce failures return an error to the caller.
type KafkaRecorder struct {
	client         *kgo.Client
	topic          string
	produceTimeout time.Duration
	// refusing tracks an outage from the first failed produce to the next success.
	refusing atomic.Bool
}

// KafkaConfig configures the producer. Brokers and Topic are required.
// Zero values use defaults for the other fields.
type KafkaConfig struct {
	// Brokers lists bootstrap addresses used to discover the cluster.
	Brokers []string

	// Topic selects the audit topic without parsing its name.
	Topic string

	// ClientID identifies the producer in broker metrics and logs.
	ClientID string

	// ProduceTimeout caps one Record call. Nonpositive values use 15 seconds.
	ProduceTimeout time.Duration
}

func applyKafkaDefaults(cfg KafkaConfig) KafkaConfig {
	if cfg.ProduceTimeout <= 0 {
		// Allow recovery across Kafka's nine-second broker lease and franz-go's
		// five-second metadata minimum age. A ten-second timeout expired before
		// recovery from single-broker failures.
		cfg.ProduceTimeout = 15 * time.Second
	}

	if cfg.ClientID == "" {
		cfg.ClientID = "tack-audit-producer"
	}

	return cfg
}

// NewKafkaRecorder constructs a client with lazy connections. Construction
// does not verify broker availability; Record returns connection failures.
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

// MarshalEvent encodes an Event as JSON without recording it. Callers can
// distinguish encoding errors from recorder side effects.
func MarshalEvent(ev Event) ([]byte, error) {
	out, err := json.Marshal(ev)
	if err != nil {
		slog.Error(
			"audit.kafka.marshal_failed",
			slog.String("verb", ev.Verb),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit kafka marshal: %w", err)
	}

	return out, nil
}

// Record publishes JSON synchronously and returns produce failures. It
// preserves EventID and OccurredAt assigned by CanonicalRecorder before
// serialization.
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
	telemetry.CountSyncProduce(ctx)
	start := monoStart()
	res := k.client.ProduceSync(produceCtx, rec)
	telemetry.DecAuditKafkaProduceInflight()
	telemetry.ObserveAuditKafkaProduceLatency(sinceMs(start))
	if produceErr := res.FirstErr(); produceErr != nil {
		telemetry.IncAuditKafkaProduce("error")
		// Log the first refusal at Warn and later refusals at Debug until
		// recovery (TACK-320). The caller records whether spill storage saved
		// the event or both recording attempts failed.
		if k.refusing.CompareAndSwap(false, true) {
			slog.WarnContext(
				ctx,
				"kafka.produce.refusing",
				slog.String("err", produceErr.Error()),
				slog.String("topic", k.topic),
				slog.String("event_id", eventID),
				slog.String("verb", ev.Verb),
			)
		} else {
			slog.DebugContext(
				ctx,
				"kafka.produce.failed",
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

	telemetry.L(ctx).Debug(
		"audit.kafka.produced",
		slog.String("verb", ev.Verb),
		slog.String("topic", k.topic),
		slog.Int("payload_bytes", len(payload)),
		slog.String("event_id", eventID),
	)

	return nil
}

// eventIDForLog prefers EventID, then IdempotencyKey, then joined actor and
// entity IDs for correlating produce failures.
func eventIDForLog(ev Event) string {
	if ev.EventID != uuid.Nil {
		return ev.EventID.String()
	}

	if ev.IdempotencyKey != "" {
		return ev.IdempotencyKey
	}

	return ev.Actor.ID.String() + ":" + ev.Entity.ID.String()
}

// Close flushes and closes the client with a background-context deadline
// independent of request cancellation, including SIGINT cancellation.
func (k *KafkaRecorder) Close() error {
	return k.CloseContext(context.Background())
}

// CloseContext flushes and closes the client within the supplied context and
// the configured produce timeout.
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

// kafkaPartitionKey encodes (org_id, shard) for Kafka partition assignment.
// Events on one chain share a partition and its single group consumer. Both
// producer and consumer derive the shard with shardOf(actor_id, event_id).
func kafkaPartitionKey(ev Event) []byte {
	shard := int(shardOf(ev.Actor.ID, ev.EventID))
	out := make([]byte, 0, 18)
	out = append(out, ev.Context.OrgID[:]...)
	out = append(out, byte((shard>>8)&0xFF), byte(shard&0xFF))

	return out
}

// SplitBrokers trims comma-separated addresses and discards empty entries.
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
