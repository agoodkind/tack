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

// relayDrainGrace limits how long Close waits for an active drain. Its
// ten-second limit is shorter than the audit-consumer's thirty-second shutdown
// budget and leaves time for the remaining shutdown work.
const relayDrainGrace = 10 * time.Second

// RelayOutboxEntry contains one FoundationDB audit outbox record awaiting
// delivery. The record can contain an event or one transport message.
type RelayOutboxEntry struct {
	// Mark is the raw versionstamped key that orders the entry.
	Mark []byte
	// Event contains the raw JSON event or transport message.
	Event json.RawMessage
	// CommitVersion is the versionstamp in Mark as hexadecimal text.
	CommitVersion string
}

// FoundationDBOutbox reads and clears FoundationDB audit outbox records.
type FoundationDBOutbox interface {
	ReadOutboxFrom(ctx context.Context, mark []byte, limit int) ([]RelayOutboxEntry, error)
	ClearThrough(ctx context.Context, mark []byte) error
}

// YugabyteOutbox reads and deletes YugabyteDB audit outbox rows. PoolOutbox
// implements the production interface.
type YugabyteOutbox interface {
	ReadBatch(ctx context.Context, limit int) ([]OutboxRow, error)
	Delete(ctx context.Context, eventID uuid.UUID) error
}

// RelayConfig collects the relay's Kafka, outbox, and polling dependencies.
type RelayConfig struct {
	// Recorder produces events to Kafka.
	Recorder Recorder
	// Yugabyte accesses the YugabyteDB audit outbox.
	Yugabyte YugabyteOutbox
	// FoundationDB reads the FoundationDB audit outbox when enabled. A nil
	// value disables that loop and runs only the YugabyteDB loop.
	FoundationDB FoundationDBOutbox
	// PollInterval controls the delay between outbox drain attempts.
	PollInterval time.Duration
	// BatchSize limits each outbox read.
	BatchSize int
	// AfterProduce optionally runs with the event ID after each broker
	// acknowledgment. An error stops the batch before deleting its current
	// YugabyteDB row or clearing any FoundationDB entry from the batch. Tests use
	// the callback to interrupt delivery before outbox removal. Production does
	// not configure the callback.
	AfterProduce func(ctx context.Context, eventID uuid.UUID) error `exhaustruct:"optional"`
	// AfterLeaseAcquired optionally runs when a poll acquires the FoundationDB
	// relay lease after a poll without the lease. Tests count lease takeovers with
	// the callback. Production does not configure the callback.
	AfterLeaseAcquired func(ctx context.Context) `exhaustruct:"optional"`
}

// Relay publishes audit records from the YugabyteDB and FoundationDB outboxes
// to Kafka.
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

// NewRelay validates the dependencies and returns a relay that can start its
// outbox loops.
//
// Recorder and Yugabyte are required. FoundationDB is optional. The constructor
// does not start the loops; [Relay.Start] starts them.
//
//   - A nonpositive PollInterval uses 250 milliseconds.
//   - A nonpositive BatchSize uses 256 records.
func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.Recorder == nil {
		slog.Error(
			"audit.relay.recorder_missing",
			slog.String("err", errRelayRecorderRequired.Error()),
		)

		return nil, fmt.Errorf("new audit relay: %w", errRelayRecorderRequired)
	}

	if cfg.Yugabyte == nil {
		slog.Error(
			"audit.relay.yugabyte_missing",
			slog.String("err", errRelayYugabyteRequired.Error()),
		)

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

// Start launches the YugabyteDB loop and the optional FoundationDB loop.
//
// The supplied context and [Relay.Close] can stop the loops.
//
//   - A waiter closes the stopped channel after all workers finish.
//   - The waiter logs a panic if its deferred recovery detects one.
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
				slog.ErrorContext(
					ctx,
					"audit.relay.wait_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)),
				)
			}
		}()
		workers.Wait()
		close(r.stopped)
	}()
}

// Close requests that the drain loops stop and closes the recorder if it
// implements Close.
//
// Close limits its wait for the loops to relayDrainGrace. Active database calls
// use their clients' timeouts; Close does not cancel those calls. An event
// remains in its outbox until a later run delivers it and removes its record.
//
//   - When the loop wait expires, Close logs a warning and continues shutdown.
//   - After the loops stop or the wait expires, Close attempts to release a
//     supported FoundationDB relay lease.
//   - Recorder closing follows lease release and can return an error.
func (r *Relay) Close() error {
	r.once.Do(func() { close(r.stop) })
	timer := time.NewTimer(relayDrainGrace)
	defer timer.Stop()
	select {
	case <-r.stopped:
	case <-timer.C:
		slog.Warn("audit.relay.drain_timeout", slog.Duration("waited", relayDrainGrace))
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
