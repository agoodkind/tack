package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	errRelayRecorderRequired = errors.New("recorder required")
	errRelayYugabyteRequired = errors.New("yugabyte outbox required")
)

// relayDrainGrace bounds how long Close waits for a drain already in flight.
// It is shorter than the audit-consumer's own thirty-second shutdown budget so
// a stuck database cannot consume the whole window.
const relayDrainGrace = 10 * time.Second

// RelayOutboxEntry is one FoundationDB operator-command audit event waiting
// for delivery.
type RelayOutboxEntry struct {
	// Mark is the raw versionstamped key that orders the entry.
	Mark []byte
	// Event is the raw JSON audit event payload.
	Event json.RawMessage
}

// FoundationDBOutbox reads and clears FoundationDB operator-command events.
type FoundationDBOutbox interface {
	ReadOutboxFrom(ctx context.Context, mark []byte, limit int) ([]RelayOutboxEntry, error)
	ClearThrough(ctx context.Context, mark []byte) error
}

// YugabyteOutbox reads and deletes YugabyteDB operator-command events.
// PoolOutbox is the production implementation.
type YugabyteOutbox interface {
	ReadBatch(ctx context.Context, limit int) ([]OutboxRow, error)
	Delete(ctx context.Context, eventID uuid.UUID) error
}

// RelayConfig collects the relay's Kafka, outbox, and polling dependencies.
type RelayConfig struct {
	// Recorder produces events to Kafka.
	Recorder Recorder
	// Yugabyte accesses the YugabyteDB operator outbox.
	Yugabyte YugabyteOutbox
	// FoundationDB reads the FoundationDB operator outbox when enabled. A nil
	// value disables that side and leaves only the YugabyteDB drain running.
	FoundationDB FoundationDBOutbox
	// PollInterval controls the delay between outbox drain attempts.
	PollInterval time.Duration
	// BatchSize limits each outbox read.
	BatchSize int
}

// Relay drains operator-command audit events from both outboxes to Kafka.
type Relay struct {
	recorder     Recorder
	yugabyte     YugabyteOutbox
	foundationDB FoundationDBOutbox
	pollInterval time.Duration
	batchSize    int
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
}

// NewRelay validates the relay dependencies and returns a ready relay.
func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.Recorder == nil {
		slog.Error("audit.relay.recorder_missing", slog.String("err", errRelayRecorderRequired.Error()))
		return nil, fmt.Errorf("new audit relay: %w", errRelayRecorderRequired)
	}
	if cfg.Yugabyte == nil {
		slog.Error("audit.relay.yugabyte_missing", slog.String("err", errRelayYugabyteRequired.Error()))
		return nil, fmt.Errorf("new audit relay: %w", errRelayYugabyteRequired)
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	return &Relay{
		recorder:     cfg.Recorder,
		yugabyte:     cfg.Yugabyte,
		foundationDB: cfg.FoundationDB,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		stop:         make(chan struct{}),
		stopped:      make(chan struct{}),
		once:         sync.Once{},
	}, nil
}

// Start runs the Yugabyte and optional FoundationDB drain loops.
func (r *Relay) Start(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Go(func() {
		r.yugabyteLoop(ctx)
	})
	if r.foundationDB != nil {
		workers.Go(func() {
			r.foundationDBLoop(ctx)
		})
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "audit.relay.wait_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)),
				)
			}
		}()
		workers.Wait()
		close(r.stopped)
	}()
}

// Close stops the drain loops and closes the recorder when it supports Close.
//
// The wait for the loops is bounded. A FoundationDB or YugabyteDB call already
// in flight blocks until its own client gives up, and that client owns the
// timeout, not this code. Waiting without a bound would let one unreachable
// database hold the whole audit-consumer open, so after the grace period Close
// stops waiting, says so, and lets shutdown continue. Nothing is lost by
// abandoning a drain: an event stays in its outbox until a later run delivers
// it.
func (r *Relay) Close() error {
	r.once.Do(func() { close(r.stop) })
	timer := time.NewTimer(relayDrainGrace)
	defer timer.Stop()
	select {
	case <-r.stopped:
	case <-timer.C:
		slog.Warn("audit.relay.drain_timeout",
			slog.Duration("waited", relayDrainGrace),
		)
	}
	closer, ok := r.recorder.(interface{ Close() error })
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		slog.Error("audit.relay.recorder_close_failed", slog.String("err", err.Error()))
		return fmt.Errorf("close audit relay recorder: %w", err)
	}
	return nil
}

func (r *Relay) yugabyteLoop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		r.drainYugabyte(ctx)
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Relay) foundationDBLoop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	var mark []byte
	for {
		mark = r.drainFoundationDB(ctx, mark)
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// drainYugabyte produces one batch and deletes each row only after the broker
// accepts it. A produce failure stops the batch and leaves the rest in place,
// so the next tick retries them; a redelivery is harmless because the consumer
// drops a duplicate event id, while a deletion before acceptance would lose an
// audit event outright.
func (r *Relay) drainYugabyte(ctx context.Context) {
	batch, err := r.yugabyte.ReadBatch(ctx, r.batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.yugabyte_read_failed", slog.String("err", err.Error()))
		return
	}
	producedCount := 0
	deletedCount := 0
	for _, row := range batch {
		if recordErr := r.recorder.Record(ctx, row.Event); recordErr != nil {
			slog.ErrorContext(ctx, "audit.relay.yugabyte_produce_failed",
				slog.String("err", recordErr.Error()))
			break
		}
		producedCount++
		if deleteErr := r.yugabyte.Delete(ctx, row.EventID); deleteErr != nil {
			slog.ErrorContext(ctx, "audit.relay.yugabyte_delete_failed",
				slog.String("err", deleteErr.Error()))
			break
		}
		deletedCount++
	}
	slog.DebugContext(ctx, "audit.relay.yugabyte_batch",
		slog.Int("read_count", len(batch)),
		slog.Int("produced_count", producedCount),
		slog.Int("deleted_count", deletedCount),
	)
}

func (r *Relay) drainFoundationDB(ctx context.Context, mark []byte) []byte {
	batch, err := r.foundationDB.ReadOutboxFrom(ctx, mark, r.batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.foundationdb_read_failed", slog.String("err", err.Error()))
		return mark
	}
	producedCount := 0
	var lastAccepted []byte
	for _, entry := range batch {
		var event Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			slog.ErrorContext(ctx, "audit.relay.foundationdb_decode_failed", slog.String("err", err.Error()))
			break
		}
		if err := r.recorder.Record(ctx, event); err != nil {
			slog.ErrorContext(ctx, "audit.relay.foundationdb_produce_failed", slog.String("err", err.Error()))
			break
		}
		producedCount++
		lastAccepted = append(lastAccepted[:0], entry.Mark...)
	}
	clearedCount := 0
	if len(lastAccepted) > 0 {
		if err := r.foundationDB.ClearThrough(ctx, lastAccepted); err != nil {
			slog.ErrorContext(ctx, "audit.relay.foundationdb_clear_failed", slog.String("err", err.Error()))
		} else {
			clearedCount = producedCount
			mark = lastAccepted
		}
	}
	slog.DebugContext(ctx, "audit.relay.foundationdb_batch",
		slog.Int("read_count", len(batch)),
		slog.Int("produced_count", producedCount),
		slog.Int("cleared_count", clearedCount),
	)
	return mark
}
