// buffered_shutdown.go delivers the buffered recorder's remaining events when
// Close stops the flusher. It retries the retained events until a shutdown
// deadline and counts each event it could not deliver by then (TACK-583).

package audit

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// closeDeliveryTimeout bounds how long Close retries the retained events.
const closeDeliveryTimeout = 30 * time.Second

// drain takes everything queued at stop time and delivers it with the
// retained events. Close locks the stop lock while it signals. Record cannot
// add an event to the queue after Close releases that lock.
func (b *BufferedRecorder) drain(ctx context.Context, batch []Event) {
	for {
		select {
		case ev := <-b.queue:
			b.room.dequeued()
			batch = append(batch, ev)
		default:
			b.retained = append(b.retained, batch...)
			b.deliverBeforeDeadline(ctx)
			return
		}
	}
}

// deliverBeforeDeadline delivers the retained events, retrying with the
// flusher's backoff until closeDeliveryTimeout passes. It ignores the backoff
// for the first attempt.
func (b *BufferedRecorder) deliverBeforeDeadline(ctx context.Context) {
	deadlineCtx, cancel := context.WithTimeout(ctx, closeDeliveryTimeout)
	defer cancel()
	for len(b.retained) > 0 {
		failed, err := b.deliverAll(deadlineCtx, b.retained)
		b.settle(ctx, failed, err)
		if len(b.retained) == 0 {
			return
		}
		wait := time.NewTimer(retryBackoff(b.attempts))
		select {
		case <-wait.C:
		case <-deadlineCtx.Done():
			wait.Stop()
			b.abandon(ctx, err)
			return
		}
	}
}

// abandon counts every retained event as lost and logs the exact count, once
// for the whole shutdown.
func (b *BufferedRecorder) abandon(ctx context.Context, err error) {
	for _, ev := range b.retained {
		telemetry.IncAuditDropped(ev.Verb, "buffer_flush")
	}
	slog.ErrorContext(ctx, "audit.buffer.flush_lost",
		slog.Int("lost", len(b.retained)),
		slog.Int("failed_attempts", b.attempts),
		slog.String("err", err.Error()))
	b.retained = nil
	b.room.setRetained(0)
}
