// batch_recorder.go is the many-events-at-once shape of the ledger backends.
// The buffered recorder flushes read-class events through it, so one round
// trip to the broker settles a whole batch instead of one event (TACK-506).

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// BatchRecorder records many events in one round trip and returns the ones
// the backend refused, so the caller can put those somewhere durable. The
// error is for a failure of the batch as a whole, never for one event.
type BatchRecorder interface {
	RecordBatch(ctx context.Context, events []Event) (refused []Event, err error)
}

// RecordBatch produces every event in one synchronous call and returns the
// events whose produce the broker refused. An event that cannot be marshaled
// is counted as dropped and left out, because no retry can change its bytes.
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

// noteRefusal logs a refused produce once per outage at Warn and at Debug
// after that, the same contract Record keeps (TACK-320).
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

// RecordBatch delivers the batch through Primary and spills every event the
// primary refused, one by one, with the same per-outage logging Record keeps.
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

// recordOneByOne is the batch shape over a primary that only records one
// event at a time.
func (s *SpillRecorder) recordOneByOne(ctx context.Context, events []Event) ([]Event, error) {
	var lost []Event
	for _, ev := range events {
		if err := s.Record(ctx, ev); err != nil {
			lost = append(lost, ev)
		}
	}
	return lost, nil
}
