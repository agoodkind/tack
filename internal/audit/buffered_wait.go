// buffered_wait.go admits a read-class event when the buffered recorder has
// no overflow and its queue is full. Record waits for room for at most
// queueWaitTimeout and does not hold the stop lock while it waits. It refuses
// the event at once when the flusher retains undelivered events, because room
// opens only after a delivery succeeds (TACK-583).

package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// queueWaitTimeout bounds how long Record waits for queue room when the
// buffered recorder has no overflow.
const queueWaitTimeout = 10 * time.Second

var (
	// errRetainedFull is the refusal of an event that found the queue full
	// while the flusher retained undelivered events.
	errRetainedFull = errors.New("audit buffer is full of undelivered events")
	// errQueueWaitTimeout is the refusal of an event that waited
	// queueWaitTimeout for queue room.
	errQueueWaitTimeout = errors.New("audit buffer queue stayed full")
)

// waitForRoom admits ev to the queue, records it with inner when Close ran
// during the wait, or returns an error when the wait cannot succeed.
func (b *BufferedRecorder) waitForRoom(ctx context.Context, ev Event) error {
	deadline := time.NewTimer(queueWaitTimeout)
	defer deadline.Stop()
	for {
		b.stopping.RLock()
		if b.closed {
			b.stopping.RUnlock()
			return b.recordThrough(ctx, ev)
		}
		result, opened := b.room.admitOrWait(ev)
		b.stopping.RUnlock()
		switch result {
		case admitted:
			return nil
		case refusedRetained:
			return b.refuseQueued(ctx, ev, errRetainedFull)
		case mustWait:
		}
		// The wait ends when room opens, Close runs, the deadline passes, or
		// ctx ends. The loop then checks the queue again.
		select {
		case <-opened:
			b.room.stopWaiting()
		case <-b.done:
			b.room.stopWaiting()
		case <-deadline.C:
			b.room.stopWaiting()
			return b.refuseQueued(ctx, ev, errQueueWaitTimeout)
		case <-ctx.Done():
			b.room.stopWaiting()
			return b.refuseQueued(ctx, ev, ctx.Err())
		}
	}
}

// refuseQueued logs and returns the refusal of one read-class event.
func (b *BufferedRecorder) refuseQueued(ctx context.Context, ev Event, cause error) error {
	slog.ErrorContext(ctx, "audit.buffer.queue_refused",
		slog.String("verb", ev.Verb),
		slog.String("event_id", ev.EventID.String()),
		slog.Int("capacity", cap(b.queue)),
		slog.String("err", cause.Error()))
	return fmt.Errorf("queue audit event %s: %w", ev.Verb, cause)
}
