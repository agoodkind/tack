package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// errRelayMessageProducer reports that the recorder does not implement messageProducer.
var errRelayMessageProducer = errors.New("relay recorder cannot publish audit transport messages")

// messageProducer lets a recorder produce raw records with an explicit Kafka key.
type messageProducer interface {
	produceMessage(ctx context.Context, key, value []byte) error
}

// produceRow records a plain row through Recorder or produces multipart messages
// in order using a Kafka key from the event's organization and shard.
func (r *Relay) produceRow(ctx context.Context, row OutboxRow) (bool, error) {
	if len(row.Messages) == 0 {
		if err := r.recorder.Record(ctx, row.Event); err != nil {
			slog.ErrorContext(ctx, "audit.relay.event_produce_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
			return false, fmt.Errorf("produce event %s: %w", row.EventID, err)
		}
		return r.stopAfterProduce(ctx, row.EventID), nil
	}
	for _, raw := range row.Messages {
		message, _, err := decodeTransportMessage(raw)
		if err != nil {
			return false, err
		}
		if err := r.produceMessage(ctx, message.partitionKey(), raw); err != nil {
			return false, err
		}
		if r.stopAfterProduce(ctx, row.EventID) {
			return true, nil
		}
	}
	return false, nil
}

// produceEntry produces one FoundationDB outbox entry as a plain event or transport message.
// It sets a missing manifest commit_version from the entry's versionstamp
// and preserves part messages unchanged.
func (r *Relay) produceEntry(ctx context.Context, entry RelayOutboxEntry) (uuid.UUID, error) {
	message, isTransport, err := decodeTransportMessage(entry.Event)
	if err != nil {
		return uuid.Nil, err
	}
	if !isTransport {
		return r.produceEvent(ctx, entry)
	}
	value := []byte(entry.Event)
	if message.Message == messageKindManifest && message.CommitVersion == "" {
		message.CommitVersion = entry.CommitVersion
		if value, err = encodeTransportMessage(message); err != nil {
			return uuid.Nil, err
		}
	}
	return message.EventID, r.produceMessage(ctx, message.partitionKey(), value)
}

// produceEvent records a plain FoundationDB event through Recorder after
// setting a missing Context.CommitVersion from the entry's versionstamp.
func (r *Relay) produceEvent(ctx context.Context, entry RelayOutboxEntry) (uuid.UUID, error) {
	var event Event
	if err := json.Unmarshal(entry.Event, &event); err != nil {
		slog.ErrorContext(ctx, "audit.relay.foundationdb_decode_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("decode FoundationDB outbox event: %w", err)
	}
	if event.Context.CommitVersion == "" {
		event.Context.CommitVersion = entry.CommitVersion
	}
	if err := r.recorder.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "audit.relay.event_produce_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("produce event %s: %w", event.EventID, err)
	}
	return event.EventID, nil
}

// produceMessage produces a raw record through the recorder's messageProducer implementation.
// It returns errRelayMessageProducer when the recorder does not implement that interface.
func (r *Relay) produceMessage(ctx context.Context, key, value []byte) error {
	producer, ok := r.recorder.(messageProducer)
	if !ok {
		return errRelayMessageProducer
	}
	if err := producer.produceMessage(ctx, key, value); err != nil {
		slog.ErrorContext(ctx, "audit.relay.message_produce_failed", slog.String("err", err.Error()))
		return fmt.Errorf("publish audit transport message: %w", err)
	}
	return nil
}

// stopAfterProduce runs AfterProduce when set and returns true if it returns an error.
// It logs the event ID and error when the function requests a stop.
func (r *Relay) stopAfterProduce(ctx context.Context, eventID uuid.UUID) bool {
	if r.afterProduce == nil {
		return false
	}
	if err := r.afterProduce(ctx, eventID); err != nil {
		slog.WarnContext(ctx, "audit.relay.stopped_after_produce",
			slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return true
	}
	return false
}

// produceMessage synchronously produces one raw Kafka record with the given event key.
func (k *KafkaRecorder) produceMessage(ctx context.Context, key, value []byte) error {
	produceCtx, cancel := context.WithTimeout(ctx, k.produceTimeout)
	defer cancel()
	telemetry.IncAuditKafkaProduceInflight()
	result := k.client.ProduceSync(produceCtx, &kgo.Record{Topic: k.topic, Key: key, Value: value})
	telemetry.DecAuditKafkaProduceInflight()
	if err := result.FirstErr(); err != nil {
		telemetry.IncAuditKafkaProduce("error")
		slog.ErrorContext(ctx, "audit.kafka.message_produce_failed", slog.String("topic", k.topic), slog.String("err", err.Error()))
		return fmt.Errorf("publish audit transport message to Kafka: %w", err)
	}
	telemetry.IncAuditKafkaProduce("ok")
	return nil
}

// partitionKey returns the organization ID bytes followed by the two shard bytes,
// using the same encoding as kafkaPartitionKey for plain events.
func (message transportMessage) partitionKey() []byte {
	key := make([]byte, 0, len(message.OrgID)+2)
	key = append(key, message.OrgID[:]...)
	return append(key, byte((message.Shard>>8)&0xFF), byte(message.Shard&0xFF))
}
