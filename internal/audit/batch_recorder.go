// batch_recorder.go defines batch recording for ledger backends. The buffered
// recorder flushes read-class events in one broker request (TACK-506).

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// BatchRecorder records multiple events in one request and returns refused
// events for durable storage by the caller. The error reports a failure of
// the whole batch rather than an individual event.
type BatchRecorder interface {
	RecordBatch(ctx context.Context, events []Event) (refused []Event, err error)
}

// RecordBatch produces every event in one synchronous call and returns events
// the broker refused. It counts encoding failures as dropped and excludes
// those events because retrying cannot change their bytes.
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

		records = append(
			records,
			&kgo.Record{Topic: k.topic, Key: kafkaPartitionKey(ev), Value: payload},
		)
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
// after that, matching Record (TACK-320).
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

// RecordBatch records through Primary and appends each refused event to the
// outbox with the same outage logging as Record.
func (s *SpillRecorder) RecordBatch(ctx context.Context, events []Event) ([]Event, error) {
	batcher, ok := s.Primary.(BatchRecorder)
	if !ok {
		return s.recordOneByOne(ctx, events)
	}

	refused, err := batcher.RecordBatch(ctx, events)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.spill.batch_failed",
			slog.Int("batch", len(events)),
			slog.String("err", err.Error()),
		)

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

// recordOneByOne records a batch with a primary that accepts one event per
// call.
func (s *SpillRecorder) recordOneByOne(ctx context.Context, events []Event) ([]Event, error) {
	var lost []Event

	for _, ev := range events {
		if err := s.Record(ctx, ev); err != nil {
			lost = append(lost, ev)
		}
	}

	return lost, nil
}
