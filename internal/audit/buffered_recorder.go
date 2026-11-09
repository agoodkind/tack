// buffered_recorder.go queues read-class audit events for a flusher to deliver
// in batches (TACK-506). The recorder stores auth.token_used before returning.
// It also records state-change events synchronously before the request returns.

package audit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// BufferedConfig sizes the buffered recorder. A zero field takes its default.
type BufferedConfig struct {
	// Capacity is how many read-class events the queue and the retained
	// undelivered events can contain together before an arriving event
	// overflows.
	Capacity int
	// BatchSize is the most events one flush delivers.
	BatchSize int
	// FlushInterval is the longest a queued event waits before a flush.
	FlushInterval time.Duration
}

const (
	defaultBufferedCapacity      = 8192
	defaultBufferedBatchSize     = 256
	defaultBufferedFlushInterval = 200 * time.Millisecond
	// batchFlushTimeout bounds one flush, produce and spill together, so a
	// stuck backend cannot hold the flusher forever.
	batchFlushTimeout = 30 * time.Second
)

// BufferedRecorder queues read-class events except auth.token_used, which it
// records before returning. When the queue is full, it appends events to the
// outbox if configured and otherwise waits for room.
type BufferedRecorder struct {
	inner         Recorder
	overflow      OutboxAppender
	queue         chan Event
	room          *bufferRoom
	batchSize     int
	flushInterval time.Duration

	// The flusher goroutine is the only reader and writer of retained,
	// attempts, and retryAt.
	retained []Event
	attempts int
	retryAt  time.Time

	stopping    sync.RWMutex
	closed      bool
	done        chan struct{}
	drained     chan struct{}
	closeOnce   sync.Once
	overflowing atomic.Bool
}

// NewBufferedRecorder creates a buffered recorder and starts its flusher.
//
// A zero or negative Capacity, BatchSize, or FlushInterval uses the package
// default for that field.
//
// The flusher uses the values from ctx. Canceling ctx does not stop it.
// The caller must call Close to stop the flusher and handle its error.
//
// When the queue is full, Record appends the event to the outbox if
// overflow is set. If overflow is nil, Record waits for room or returns
// an error.
//
//	recorder := NewBufferedRecorder(ctx, inner, nil, BufferedConfig{})
//	err := recorder.Close()
func NewBufferedRecorder(ctx context.Context, inner Recorder, overflow OutboxAppender, cfg BufferedConfig) *BufferedRecorder {
	if cfg.Capacity <= 0 {
		cfg.Capacity = defaultBufferedCapacity
	}

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBufferedBatchSize
	}

	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultBufferedFlushInterval
	}
	queue := make(chan Event, cfg.Capacity)
	b := &BufferedRecorder{
		inner:         inner,
		overflow:      overflow,
		queue:         queue,
		room:          newBufferRoom(queue),
		batchSize:     cfg.BatchSize,
		flushInterval: cfg.FlushInterval,
		retained:      nil,
		attempts:      0,
		retryAt:       time.Time{},
		stopping:      sync.RWMutex{},
		closed:        false,
		done:          make(chan struct{}),
		drained:       make(chan struct{}),
		closeOnce:     sync.Once{},
		overflowing:   atomic.Bool{},
	}
	flusherCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("audit.buffer.flusher_panicked", slog.Any("err", r))
			}

			close(b.drained)
		}()

		b.run(flusherCtx)
	}()

	return b
}

// Record records an audit event or queues it for delivery.
//
// Record queues read-class events except auth.token_used. It records
// auth.token_used in the outbox when configured and otherwise calls the
// inner recorder. It calls the inner recorder for non-read-class events.
// After Close, it calls the inner recorder for other read-class events.
//
// When the queue is full, Record appends the event to the outbox if
// configured. Without an outbox, it waits for room or returns an error
// if retained events prevent admission, the wait expires, or ctx ends.
//
//	err := b.Record(ctx, event)
func (b *BufferedRecorder) Record(ctx context.Context, event Event) error {
	if !IsRead(Verb(event.Verb)) {
		return b.recordThrough(ctx, event)
	}

	if needsDurableAdmission(Verb(event.Verb)) {
		return b.recordDurably(ctx, event)
	}

	b.stopping.RLock()

	if b.closed {
		b.stopping.RUnlock()

		return b.recordThrough(ctx, event)
	}

	if b.room.tryAdmit(event) {
		b.stopping.RUnlock()
		b.noteRoom(ctx)

		return nil
	}

	b.stopping.RUnlock()

	if b.overflow == nil {
		return b.waitForRoom(ctx, event)
	}

	return b.overflowed(ctx, event)
}

// recordThrough hands one event to inner on the caller's own path.
func (b *BufferedRecorder) recordThrough(ctx context.Context, ev Event) error {
	if err := b.inner.Record(ctx, ev); err != nil {
		slog.ErrorContext(ctx, "audit.buffer.record_failed",
			slog.String("verb", ev.Verb), slog.String("err", err.Error()))

		return fmt.Errorf("record %s through buffer: %w", ev.Verb, err)
	}

	return nil
}
