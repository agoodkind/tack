// buffered_overflow.go appends a read-class event that finds the buffered
// recorder's queue full to the durable outbox (TACK-506).

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// outboxAppendFailure specifies the log event, failure stages, and error text
// for one kind of outbox append.
type outboxAppendFailure struct {
	logEvent     string
	marshalStage string
	appendStage  string
	appendError  string
}

var (
	// overflowFailure is the report of an append for an event that found the queue full.
	overflowFailure = outboxAppendFailure{
		logEvent: "audit.buffer.overflow_failed", marshalStage: "overflow_marshal",
		appendStage: "overflow", appendError: "audit overflow to outbox",
	}
	// durableFailure is the report of an append for an event that needs durable admission.
	durableFailure = outboxAppendFailure{
		logEvent: "audit.durable_admission.failed", marshalStage: "durable_admission_marshal",
		appendStage: "durable_admission", appendError: "audit durable admission to outbox",
	}
)

// overflowed appends an event that the queue could not admit to the outbox.
// It logs once per overload rather than once per event.
func (b *BufferedRecorder) overflowed(ctx context.Context, ev Event) error {
	if err := b.appendToOutbox(ctx, ev, overflowFailure); err != nil {
		return err
	}
	telemetry.IncAuditBufferOverflow(ev.Verb)
	if b.overflowing.CompareAndSwap(false, true) {
		slog.WarnContext(ctx, "audit.buffer.overflowing",
			slog.String("verb", ev.Verb),
			slog.Int("capacity", cap(b.queue)))
	}
	return nil
}

// recordDurably appends the event to the outbox when configured and otherwise
// records it synchronously through inner.
func (b *BufferedRecorder) recordDurably(ctx context.Context, ev Event) error {
	if b.overflow == nil {
		return b.recordThrough(ctx, ev)
	}
	return b.appendToOutbox(ctx, ev, durableFailure)
}

func (b *BufferedRecorder) appendToOutbox(ctx context.Context, ev Event, failure outboxAppendFailure) error {
	payload, err := MarshalEvent(ev)
	if err != nil {
		telemetry.IncAuditDropped(ev.Verb, failure.marshalStage)
		return fmt.Errorf("marshal %s event for outbox: %w", ev.Verb, err)
	}
	spillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SpillTimeout)
	defer cancel()
	if err := b.overflow.Append(spillCtx, payload); err != nil {
		telemetry.IncAuditDropped(ev.Verb, failure.appendStage)
		slog.ErrorContext(ctx, failure.logEvent,
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", failure.appendError, err)
	}
	return nil
}

// noteRoom logs the end of an overload when the queue admits events again.
func (b *BufferedRecorder) noteRoom(ctx context.Context) {
	if b.overflowing.CompareAndSwap(true, false) {
		slog.InfoContext(ctx, "audit.buffer.overflow_ended")
	}
}
