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

	"goodkind.io/tack/internal/telemetry"
)

// BufferedConfig sizes the buffered recorder. A zero field takes its default.
type BufferedConfig struct {
	// Capacity is how many read-class events the queue holds before an
	// arriving event overflows.
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
// through inner. An event that finds the queue full goes to overflow, the
// durable outbox the relay drains, so the request never waits on the broker
// and nothing is lost; with no overflow configured the request waits for room.
type BufferedRecorder struct {
	inner         Recorder
	overflow      OutboxAppender
	queue         chan Event
	batchSize     int
	flushInterval time.Duration

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
	b := &BufferedRecorder{
		inner:         inner,
		overflow:      overflow,
		queue:         make(chan Event, cfg.Capacity),
		batchSize:     cfg.BatchSize,
		flushInterval: cfg.FlushInterval,
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
	select {
	case b.queue <- ev:
		b.stopping.RUnlock()
		b.noteRoom(ctx)
		return nil
	default:
	}
	if b.overflow == nil {
		defer b.stopping.RUnlock()
		select {
		case b.queue <- ev:
			return nil
		case <-ctx.Done():
			slog.WarnContext(ctx, "audit.buffer.queue_wait_abandoned",
				slog.String("verb", ev.Verb), slog.String("err", ctx.Err().Error()))
			return fmt.Errorf("queue audit event %s: %w", ev.Verb, ctx.Err())
		}
	}
	b.stopping.RUnlock()
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

// overflowed puts an event the queue could not take into the outbox, logging
// once per overload rather than once per event.
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

// noteRoom closes an overload once the queue takes events again.
func (b *BufferedRecorder) noteRoom(ctx context.Context) {
	if b.overflowing.CompareAndSwap(true, false) {
		slog.InfoContext(ctx, "audit.buffer.overflow_ended")
	}
}

// run is the flusher: it delivers a batch when it is full or when the
// interval passes, and drains the queue when Close asks it to stop.
func (b *BufferedRecorder) run(ctx context.Context) {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, b.batchSize)
	for {
		select {
		case ev := <-b.queue:
			batch = append(batch, ev)
			if len(batch) >= b.batchSize {
				b.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				b.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-b.done:
			b.drain(ctx, batch)
			return
		}
	}
}

// drain flushes everything queued at stop time. Close holds the stop lock
// while it signals, so no event can enter the queue after this reads it empty.
func (b *BufferedRecorder) drain(ctx context.Context, batch []Event) {
	for {
		select {
		case ev := <-b.queue:
			batch = append(batch, ev)
			if len(batch) >= b.batchSize {
				b.flush(ctx, batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				b.flush(ctx, batch)
			}
			return
		}
	}
}

// flush delivers one batch through inner, in one round trip when inner can,
// and counts what neither the backend nor the spill would take.
func (b *BufferedRecorder) flush(parent context.Context, batch []Event) {
	ctx, cancel := context.WithTimeout(parent, batchFlushTimeout)
	defer cancel()
	lost, err := b.deliver(ctx, batch)
	for _, ev := range lost {
		telemetry.IncAuditDropped(ev.Verb, "buffer_flush")
	}
	if len(lost) > 0 {
		if err == nil {
			err = errBrokerRefused
		}
		slog.ErrorContext(ctx, "audit.buffer.flush_lost",
			slog.Int("lost", len(lost)),
			slog.Int("batch", len(batch)),
			slog.String("err", err.Error()))
	}
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

// Close stops taking events into the queue, delivers what is queued, then
// closes inner when it supports closing. Events recorded after Close go
// through inner directly.
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
