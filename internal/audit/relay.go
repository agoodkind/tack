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
	// CommitVersion is the versionstamp in Mark as hexadecimal text.
	CommitVersion string
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
	// AfterProduce optionally runs with the event ID after each broker acknowledgment.
	// An error stops the current batch before the relay deletes its current
	// YugabyteDB row or clears any FoundationDB entry in that batch.
	// Tests use it to interrupt delivery before outbox removal; production does not set it.
	AfterProduce func(ctx context.Context, eventID uuid.UUID) error `exhaustruct:"optional"`
	// AfterLeaseAcquired optionally runs when the relay acquires the
	// FoundationDB relay lease after a poll in which it did not hold the lease.
	// Tests use it to count lease takeovers; production does not set it.
	AfterLeaseAcquired func(ctx context.Context) `exhaustruct:"optional"`
}

// Relay drains operator-command audit events from both outboxes to Kafka.
type Relay struct {
	recorder     Recorder
	yugabyte     YugabyteOutbox
	foundationDB FoundationDBOutbox
	pollInterval time.Duration
	batchSize    int
	afterProduce func(ctx context.Context, eventID uuid.UUID) error
	afterLease   func(ctx context.Context)
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
		afterProduce: cfg.AfterProduce,
		afterLease:   cfg.AfterLeaseAcquired,
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
//
// Close attempts to release the FoundationDB relay lease after the drain
// loops stop or the wait expires, if the outbox supports leases.
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
	r.releaseLease()
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
