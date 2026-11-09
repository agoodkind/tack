// The buffered recorder submits read-class events in batches.
// The ledger backends support one broker round trip per batch (TACK-506).

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// BatchRecorder records multiple events in one round trip and returns the
// events the backend refuses. The caller must store those events durably.
// The error reports a failure of the whole batch, not an individual event.
type BatchRecorder interface {
	RecordBatch(ctx context.Context, events []Event) (refused []Event, err error)
}

// RecordBatch submits the serializable events in one synchronous broker call.
//
// RecordBatch returns the events the broker refuses. It counts events that
// MarshalEvent cannot encode as dropped and excludes them from the batch.
// Retrying the same invalid event does not repair its encoding.
//
// The batch handles failures as follows:
//
//   - Broker refusals return the affected events for durable storage.
//   - Encoding failures increment the dropped-event count without returning
//     the affected events.
func (k *KafkaRecorder) RecordBatch(ctx context.Context, events []Event) ([]Event, error) {
	records := make([]*kgo.Record, 0, len(events))
	produced := make([]Event, 0, len(events))
	for _, ev := range events {
		payload, err := MarshalEvent(ev)
		if err != nil {
			telemetry.IncAuditDropped(ev.Verb, "kafka_marshal")
			telemetry.IncAuditKafkaProduce("error")
			continue
		}
		records = append(records, &kgo.Record{Topic: k.topic, Key: kafkaPartitionKey(ev), Value: payload})
		produced = append(produced, ev)
	}
	if len(records) == 0 {
		return nil, nil
	}
	produceCtx, cancel := context.WithTimeout(ctx, k.produceTimeout)
	defer cancel()
	start := monoStart()
	results := k.client.ProduceSync(produceCtx, records...)
	telemetry.ObserveAuditKafkaProduceLatency(sinceMs(start))

	var refused []Event
	for index, result := range results {
		if result.Err == nil {
			telemetry.IncAuditKafkaProduce("ok")
			continue
		}
		telemetry.IncAuditKafkaProduce("error")
		refused = append(refused, produced[index])
		k.noteRefusal(ctx, produced[index], result.Err)
	}
	if len(refused) == 0 && k.refusing.CompareAndSwap(true, false) {
		slog.InfoContext(ctx, "kafka.produce.recovered", slog.String("topic", k.topic))
	}
	return refused, nil
}

// noteRefusal logs the first broker refusal of each outage at Warn and later
// refusals at Debug. Record uses the same logging contract (TACK-320).
func (k *KafkaRecorder) noteRefusal(ctx context.Context, ev Event, produceErr error) {
	attrs := []slog.Attr{
		slog.String("err", produceErr.Error()),
		slog.String("topic", k.topic),
		slog.String("event_id", eventIDForLog(ev)),
		slog.String("verb", ev.Verb),
	}
	if k.refusing.CompareAndSwap(false, true) {
		slog.LogAttrs(ctx, slog.LevelWarn, "kafka.produce.refusing", attrs...)
		return
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "kafka.produce.failed", attrs...)
}

// RecordBatch submits the batch to Primary and appends each refused event to
// the spill individually.
//
// RecordBatch uses the same per-outage logging as Record. It returns the
// events that both Primary and the spill refuse.
//
// The Primary implementation controls batch submission:
//
//   - A BatchRecorder receives one RecordBatch call.
//   - A recorder without batch support receives one Record call per event.
func (s *SpillRecorder) RecordBatch(ctx context.Context, events []Event) ([]Event, error) {
	batcher, ok := s.Primary.(BatchRecorder)
	if !ok {
		return s.recordOneByOne(ctx, events)
	}
	refused, err := batcher.RecordBatch(ctx, events)
	if err != nil {
		slog.ErrorContext(ctx, "audit.spill.batch_failed",
			slog.Int("batch", len(events)), slog.String("err", err.Error()))
		return nil, fmt.Errorf("batch through primary: %w", err)
	}
	if len(refused) == 0 {
		s.noteRecovered(ctx)
		return nil, nil
	}
	var lost []Event
	for _, ev := range refused {
		if spillErr := s.spill(ctx, ev, errBrokerRefused); spillErr != nil {
			lost = append(lost, ev)
		}
	}
	return lost, nil
}

// recordOneByOne records each event separately when Primary does not support
// batch recording.
func (s *SpillRecorder) recordOneByOne(ctx context.Context, events []Event) ([]Event, error) {
	var lost []Event
	for _, ev := range events {
		if err := s.Record(ctx, ev); err != nil {
			lost = append(lost, ev)
		}
	}
	return lost, nil
}
