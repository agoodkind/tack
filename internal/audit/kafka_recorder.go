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

// KafkaRecorder publishes JSON audit events to an Apache Kafka topic. The
// audit-consumer writes those events to canonical audit.events rows as part
// of the audit cutover.
//
// KafkaRecorder produces synchronously with acks=all and idempotence enabled.
// It returns errors to the caller and does not silently discard events.
type KafkaRecorder struct {
	client         *kgo.Client
	topic          string
	produceTimeout time.Duration
	// refusing is true after the first refused produce until the next accepted
	// one. The flag limits outage warnings to one per failure interval.
	refusing atomic.Bool
}

// KafkaConfig configures the producer for a deployment. Brokers and Topic
// are required. Other fields use defaults when empty or zero.
type KafkaConfig struct {
	// Brokers lists bootstrap broker addresses. The franz-go client uses these
	// addresses to discover the rest of the cluster at runtime. The list supports
	// both one broker and multiple brokers.
	Brokers []string

	// Topic selects the Kafka topic. The Phase 2 design specifies audit.events.v1;
	// the producer treats the topic value as opaque.
	Topic string

	// ClientID labels the producer in broker-side metrics and logs.
	ClientID string

	// ProduceTimeout limits one Record call. Nonpositive values use 15s.
	ProduceTimeout time.Duration
}

func applyKafkaDefaults(cfg KafkaConfig) KafkaConfig {
	if cfg.ProduceTimeout <= 0 {
		// The 15s timeout allows recovery after a hard broker loss. This repo does
		// not override the external broker.session.timeout.ms default of 9s for the
		// broker lease or franz-go's external 5s metadata minimum age. Recovery can
		// consume both waits. The former 10s timeout expired within that interval and
		// caused spurious Record errors during single-broker failures.
		cfg.ProduceTimeout = 15 * time.Second
	}

	if cfg.ClientID == "" {
		cfg.ClientID = "tack-audit-producer"
	}

	return cfg
}

// NewKafkaRecorder creates a KafkaRecorder without waiting for connections.
//
// The franz-go client connects lazily. Construction can succeed when a
// broker is unavailable. [KafkaRecorder.Record] reports connection failures
// if publication fails. [KafkaRecorder.Ready] checks readiness before use.
//
// A caller can construct the recorder as follows:
//
//	config := KafkaConfig{
//		Brokers: []string{"broker-a:9092", "broker-b:9092"},
//		Topic:   "audit.events.v1",
//	}
//	recorder, err := NewKafkaRecorder(config)
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

// MarshalEvent encodes an Event as JSON.
//
// The helper wraps [encoding/json.Marshal] errors as encoding errors without
// recording an event. [KafkaRecorder.Record] and tests can invoke the encoder
// directly.
//
// A caller can encode an event as follows:
//
//	payload, err := MarshalEvent(Event{Verb: "node.read"})
//	// Encoding alone does not publish payload to Kafka.
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

// Record encodes ev as JSON and publishes it synchronously.
//
// The method uses the configured topic and returns broker errors. It
// preserves EventID and OccurredAt from ev. [CanonicalRecorder] sets missing
// values at the recording call. The Kafka payload and consumer chain hash
// use the same identity and timestamp.
//
// A caller can publish an event as follows:
//
//	event.EventID = uuid.Must(uuid.NewV7())
//	event.OccurredAt = clock.Now().UTC()
//	err := recorder.Record(ctx, event)
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
		// The first refusal after success logs Warn with the broker error. Later
		// refusals log Debug until recovery, which logs Info. An hour-long outage
		// therefore produces one Warn and one recovery Info instead of one Error
		// per write. TACK-320 tracked that former log flood. The caller determines
		// the refused event's outcome. A successful SpillRecorder fallback saves
		// the event durably. A caller without storage logs its loss.
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

// eventIDForLog returns a stable identifier that correlates a produce
// failure with downstream evidence. It prefers EventID, then IdempotencyKey,
// then the actor and entity IDs joined by a colon. That fallback cannot
// produce an empty identifier.
func eventIDForLog(ev Event) string {
	if ev.EventID != uuid.Nil {
		return ev.EventID.String()
	}

	if ev.IdempotencyKey != "" {
		return ev.IdempotencyKey
	}

	return ev.Actor.ID.String() + ":" + ev.Entity.ID.String()
}

// Close flushes the producer queue and closes the client.
//
// The parameterless method supports the server shutdown type assertion. It
// uses [context.Background] for the flush deadline even when SIGINT has
// canceled the caller's context.
//
// A caller can close the recorder as follows:
//
//	// Close can flush after the caller cancels the request context.
//	err := recorder.Close()
func (k *KafkaRecorder) Close() error {
	return k.CloseContext(context.Background())
}

// CloseContext flushes the producer queue and closes the client.
//
// The method uses ctx and the configured produce timeout to limit the flush.
// It closes the client even when flushing fails and returns the flush error.
//
// A caller can limit shutdown with a context:
//
//	shutdown, cancel := context.WithTimeout(ctx, time.Second)
//	defer cancel()
//	err := recorder.CloseContext(shutdown)
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

// kafkaPartitionKey encodes org_id and shard as the producer key. Events on
// one organization and shard chain use the same partition. Kafka assigns
// each partition to one consumer within a group, and that consumer advances
// the chain. The producer and consumer both use shardOf(actor_id, event_id)
// to select the shard.
func kafkaPartitionKey(ev Event) []byte {
	shard := int(shardOf(ev.Actor.ID, ev.EventID))
	out := make([]byte, 0, 18)
	out = append(out, ev.Context.OrgID[:]...)
	out = append(out, byte((shard>>8)&0xFF), byte(shard&0xFF))

	return out
}

// SplitBrokers parses a comma-separated broker list.
//
// The server setup uses this helper to trim surrounding whitespace and omit
// empty entries.
//
// A caller can parse the list as follows:
//
//	brokers := SplitBrokers(" broker-a:9092, , broker-b:9092 ")
//	fmt.Println(brokers)
//	// Output: [broker-a:9092 broker-b:9092]
func SplitBrokers(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		out = append(out, part)
	}

	return out
}
