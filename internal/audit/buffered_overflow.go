// buffered_overflow.go appends a read-class event that finds the buffered
// recorder's queue full to the durable outbox (TACK-506).

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// overflowed appends an event that the queue could not admit to the outbox.
// It logs once per overload rather than once per event.
func (b *BufferedRecorder) overflowed(ctx context.Context, ev Event) error {
	payload, err := MarshalEvent(ev)
	if err != nil {
		telemetry.IncAuditDropped(ev.Verb, "overflow_marshal")
		return err
	}
	spillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spillTimeout)
	defer cancel()
	if err := b.overflow.Append(spillCtx, payload); err != nil {
		telemetry.IncAuditDropped(ev.Verb, "overflow")
		slog.ErrorContext(ctx, "audit.buffer.overflow_failed",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("audit overflow to outbox: %w", err)
	}
	telemetry.IncAuditBufferOverflow(ev.Verb)
	if b.overflowing.CompareAndSwap(false, true) {
		slog.WarnContext(ctx, "audit.buffer.overflowing",
			slog.String("verb", ev.Verb),
			slog.Int("capacity", cap(b.queue)))
	}
	return nil
}

// noteRoom logs the end of an overload when the queue admits events again.
func (b *BufferedRecorder) noteRoom(ctx context.Context) {
	if b.overflowing.CompareAndSwap(true, false) {
		slog.InfoContext(ctx, "audit.buffer.overflow_ended")
	}
}
