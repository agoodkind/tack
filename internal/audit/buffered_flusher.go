// buffered_flusher.go is the buffered recorder's flusher goroutine. It
// delivers queued read-class events in batches through the inner recorder and
// retains every event that neither the primary nor the spill accepted. It
// delivers the retained events again after a bounded backoff with the same
// event IDs. The consumer's event-ID claim prevents a duplicate ledger row
// when a retry delivers an event twice (TACK-583).

package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	// retryBackoffMin is the wait before the first retry of retained events.
	retryBackoffMin = 500 * time.Millisecond
	// retryBackoffMax bounds the wait between retries of retained events.
	retryBackoffMax = 30 * time.Second
	// retryBackoffMaxDoublings stops the doubling before the shift overflows.
	retryBackoffMaxDoublings = 16
)

// run is the flusher: it delivers a batch when it is full or when the
// interval passes, and drains the queue when Close asks it to stop.
func (b *BufferedRecorder) run(ctx context.Context) {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, b.batchSize)
	for {
		select {
		case ev := <-b.queue:
			b.room.dequeued()
			batch = append(batch, ev)
			if len(batch) >= b.batchSize {
				b.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 || len(b.retained) > 0 {
				b.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-b.done:
			b.drain(ctx, batch)
			return
		}
	}
}

// flush adds batch to the retained events and delivers them when the backoff
// after the last failed attempt has passed. Before that time, the batch waits
// with the retained events and counts against the queue capacity.
func (b *BufferedRecorder) flush(ctx context.Context, batch []Event) {
	b.retained = append(b.retained, batch...)
	if len(b.retained) == 0 {
		return
	}
	if b.attempts > 0 && clock.Now().Before(b.retryAt) {
		b.room.setRetained(len(b.retained))
		return
	}
	failed, err := b.deliverAll(ctx, b.retained)
	b.settle(ctx, failed, err)
}

// deliverAll delivers events in chunks of the batch size. It stops at the
// first chunk with an undelivered event and returns that chunk's undelivered
// events followed by every chunk it did not attempt.
func (b *BufferedRecorder) deliverAll(ctx context.Context, events []Event) ([]Event, error) {
	for start := 0; start < len(events); start += b.batchSize {
		end := min(start+b.batchSize, len(events))
		chunkCtx, cancel := context.WithTimeout(ctx, batchFlushTimeout)
		lost, err := b.deliver(chunkCtx, events[start:end])
		cancel()
		if len(lost) == 0 {
			continue
		}
		if err == nil {
			err = errBrokerRefused
		}
		failed := make([]Event, 0, len(lost)+len(events)-end)
		failed = append(failed, lost...)
		failed = append(failed, events[end:]...)
		return failed, err
	}
	return nil, nil
}

func (b *BufferedRecorder) deliver(ctx context.Context, batch []Event) ([]Event, error) {
	if batcher, ok := b.inner.(BatchRecorder); ok {
		refused, err := batcher.RecordBatch(ctx, batch)
		if err != nil {
			slog.ErrorContext(ctx, "audit.buffer.batch_failed",
				slog.Int("batch", len(batch)), slog.String("err", err.Error()))
			return batch, fmt.Errorf("deliver audit batch: %w", err)
		}
		return refused, nil
	}
	var lost []Event
	for _, ev := range batch {
		if err := b.inner.Record(ctx, ev); err != nil {
			lost = append(lost, ev)
		}
	}
	return lost, nil
}

// settle keeps the events of an attempt that the inner recorder did not
// accept, and schedules the next attempt. It logs once per outage at Error
// when events are first retained, at Debug for each later failed attempt, and
// at Info when a later attempt delivers every retained event.
func (b *BufferedRecorder) settle(ctx context.Context, failed []Event, err error) {
	b.retained = failed
	b.room.setRetained(len(failed))
	if len(failed) == 0 {
		if b.attempts > 0 {
			slog.InfoContext(ctx, "audit.buffer.retained_delivered",
				slog.Int("failed_attempts", b.attempts))
		}
		b.attempts = 0
		return
	}
	b.attempts++
	b.retryAt = clock.Now().Add(retryBackoff(b.attempts))
	if b.attempts == 1 {
		slog.ErrorContext(ctx, "audit.buffer.delivery_retained",
			slog.Int("retained", len(failed)),
			slog.String("err", err.Error()))
		return
	}
	slog.DebugContext(ctx, "audit.buffer.retry_failed",
		slog.Int("retained", len(failed)),
		slog.Int("failed_attempts", b.attempts),
		slog.String("err", err.Error()))
}

// retryBackoff returns the wait after the given count of failed attempts. The
// wait doubles from retryBackoffMin and stops at retryBackoffMax.
func retryBackoff(attempts int) time.Duration {
	doublings := min(max(attempts-1, 0), retryBackoffMaxDoublings)
	return min(retryBackoffMin<<doublings, retryBackoffMax)
}
