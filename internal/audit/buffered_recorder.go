// buffered_recorder.go takes read-class audit events off the request path: a
// request hands its event to an in-process queue and answers, and a flusher
// delivers the queue in batches (TACK-506). State-change events keep their
// synchronous path, because a write must not answer before its ledger row is
// on its way.

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

// BufferedRecorder queues read-class events and delivers them in batches
// through inner. The flusher retains each event that inner did not accept and
// delivers it again later. Record appends an event that finds the queue full
// to overflow, the durable outbox the relay drains, and returns an error when
// that append fails. With no overflow configured the request waits for room.
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

// NewBufferedRecorder starts the flusher. ctx's values, not its cancellation,
// reach the flusher's own contexts. overflow may be nil.
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

// Record queues a read-class event and returns at once, and records any other
// event through inner before returning.
func (b *BufferedRecorder) Record(ctx context.Context, ev Event) error {
	if !IsRead(Verb(ev.Verb)) {
		return b.recordThrough(ctx, ev)
	}
	b.stopping.RLock()
	if b.closed {
		b.stopping.RUnlock()
		return b.recordThrough(ctx, ev)
	}
	if b.room.tryAdmit(ev) {
		b.stopping.RUnlock()
		b.noteRoom(ctx)
		return nil
	}
	b.stopping.RUnlock()
	if b.overflow == nil {
		return b.waitForRoom(ctx, ev)
	}
	return b.overflowed(ctx, ev)
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

// Close stops taking events into the queue, delivers the queued and retained
// events within closeDeliveryTimeout, then closes inner when it supports
// closing. Record calls inner.Record directly for each event recorded after
// Close.
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
		if err := recorder.Close(); err != nil {
			slog.Error("audit.buffer.inner_close_failed", slog.String("err", err.Error()))
			return fmt.Errorf("close buffered inner: %w", err)
		}
	case interface{ Close() }:
		recorder.Close()
	}
	return nil
}
