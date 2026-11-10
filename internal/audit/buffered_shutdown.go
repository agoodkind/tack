// Close stops the flusher and attempts delivery of the remaining events.
// Shutdown retries retained events until the deadline and counts events it
// cannot deliver by then (TACK-583).

package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// closeDeliveryTimeout bounds how long Close retries the retained events.
const closeDeliveryTimeout = 30 * time.Second

// Close stops the buffered recorder's flusher and closes inner if supported.
//
// Close stops queue admission and waits for the flusher to drain queued
// and retained events. The flusher retries delivery until it succeeds or
// closeDeliveryTimeout expires. It logs and counts any events still
// undelivered after the deadline.
//
// After Close stops queue admission, Record sends ordinary read-class
// events directly to inner. Record also uses inner for calls waiting for
// queue room when Close stops admission. Each Close call waits for the
// flusher and calls inner.Close if inner supports it. Close wraps and
// returns an error from inner.Close; it does not return delivery errors.
//
//	err := b.Close()
func (b *BufferedRecorder) Close() error {
	b.closeOnce.Do(func() {
		b.stopping.Lock()
		b.closed = true
		close(b.done)
		b.stopping.Unlock()
	})
	<-b.drained

	switch recorder := b.inner.(type) {
	case interface{ Close() error }:
		err := recorder.Close()
		if err != nil {
			slog.Error("audit.buffer.inner_close_failed", slog.String("err", err.Error()))
			return fmt.Errorf("close buffered inner: %w", err)
		}
	case interface{ Close() }:
		recorder.Close()
	}

	return nil
}

// drain collects the events queued at stop time and delivers them with the
// retained events. Close signals while locking stopping. Record cannot add an
// event to the queue after Close releases that lock.
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

	slog.ErrorContext(
		ctx,
		"audit.buffer.flush_lost",
		slog.Int("lost", len(b.retained)),
		slog.Int("failed_attempts", b.attempts),
		slog.String("err", err.Error()),
	)
	b.retained = nil
	b.room.setRetained(0)
}
