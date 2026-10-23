// Package auditrelay builds the relay that the audit-consumer runs. The relay
// reads the FoundationDB and YugabyteDB operator outboxes and produces each
// event to the Kafka audit topic.
package auditrelay

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

// Config provides the dependencies for the relay.
type Config struct {
	// Brokers lists the Kafka bootstrap brokers.
	Brokers []string
	// Topic is the Kafka audit topic.
	Topic string
	// YugabyteDSN is the connection string for the pool that reads the
	// YugabyteDB operator outbox.
	YugabyteDSN string
	// FDBClusterFile enables the FoundationDB operator outbox reader when its
	// value is nonempty.
	FDBClusterFile string
	// FDBTransactionTimeout limits the duration of each FoundationDB relay
	// transaction, including retries.
	FDBTransactionTimeout time.Duration
	// PollInterval sets the delay between outbox drain attempts.
	PollInterval time.Duration
	// BatchSize limits the number of entries in each outbox read.
	BatchSize int
	// LeaseDuration sets the FoundationDB relay lease duration. Open uses
	// DefaultLeaseDuration when the value is not positive.
	LeaseDuration time.Duration `exhaustruct:"optional"`
	// AfterProduce runs after each broker acknowledgment when set. An error stops
	// the batch before the relay deletes the current YugabyteDB row or clears the
	// current FoundationDB batch. Production does not set this test callback.
	AfterProduce func(ctx context.Context, eventID uuid.UUID) error `exhaustruct:"optional"`
	// AfterLeaseAcquired runs when the relay acquires the FoundationDB relay
	// lease after a poll in which it did not hold the lease. Production does not
	// set this test callback.
	AfterLeaseAcquired func(ctx context.Context) `exhaustruct:"optional"`
}

// DefaultLeaseDuration sets the FoundationDB relay lease duration when
// Config.LeaseDuration is not positive.
const DefaultLeaseDuration = 10 * time.Second

// Opened contains an initialized Relay (*audit.Relay) and the YugabyteDB Pool
// and Kafka Recorder that Relay uses. The caller starts Relay, later closes
// Relay, and then closes Pool. Relay.Close also closes Recorder.
type Opened struct {
	// Relay drains both operator outboxes.
	Relay *audit.Relay
	// Pool reads the YugabyteDB operator outbox.
	Pool *pgxpool.Pool
	// Recorder produces the relayed events to Kafka.
	Recorder *audit.KafkaRecorder
}

type foundationDBOutbox struct {
	store *foundationdb.LeasedOpsOutbox
}

func (s foundationDBOutbox) HoldRelayLease(ctx context.Context) (bool, error) {
	held, err := s.store.Hold(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_lease_hold_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("acquire or renew the FoundationDB relay lease: %w", err)
	}
	return held, nil
}

func (s foundationDBOutbox) ReleaseRelayLease(ctx context.Context) error {
	if err := s.store.Release(ctx); err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_lease_release_failed", slog.String("err", err.Error()))
		return fmt.Errorf("release the FoundationDB relay lease: %w", err)
	}
	return nil
}

func (s foundationDBOutbox) ReadOutboxFrom(ctx context.Context, mark []byte, limit int) ([]audit.RelayOutboxEntry, error) {
	entries, err := s.store.ReadOutboxFrom(ctx, mark, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_fdb_read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read FoundationDB relay outbox: %w", err)
	}
	out := make([]audit.RelayOutboxEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, audit.RelayOutboxEntry{Mark: entry.Mark, Event: entry.Event, CommitVersion: entry.CommitVersion})
	}
	return out, nil
}

func (s foundationDBOutbox) ClearThrough(ctx context.Context, mark []byte) error {
	if err := s.store.ClearThrough(ctx, mark); err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_fdb_clear_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clear FoundationDB relay outbox: %w", err)
	}
	return nil
}

func (s foundationDBOutbox) ClearThroughPublished(ctx context.Context, mark []byte, reads []audit.PublishedRead) error {
	if err := s.store.ClearThroughPublished(ctx, mark, reads); err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_fdb_clear_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clear FoundationDB relay outbox and mark reads published: %w", err)
	}
	return nil
}

// Open builds the relay from cfg. Tests and cmd/audit-consumer use this
// function.
func Open(ctx context.Context, cfg Config) (Opened, error) {
	none := Opened{Relay: nil, Pool: nil, Recorder: nil}
	pool, err := pgxpool.New(ctx, cfg.YugabyteDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_yugabyte_pool_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("new relay Yugabyte pool: %w", err)
	}
	var relayFoundationDB audit.FoundationDBOutbox
	if cfg.FDBClusterFile != "" {
		stores, storesErr := foundationdb.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, pool)
		if storesErr != nil {
			pool.Close()
			slog.ErrorContext(ctx, "audit_consumer.relay_foundationdb_open_failed", slog.String("err", storesErr.Error()))
			return none, fmt.Errorf("open relay FoundationDB stores: %w", storesErr)
		}
		leaseDuration := cfg.LeaseDuration
		if leaseDuration <= 0 {
			leaseDuration = DefaultLeaseDuration
		}
		relayFoundationDB = foundationDBOutbox{store: stores.OpsOutbox.Leased(uuid.New(), leaseDuration)}
	}
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: cfg.Brokers, Topic: cfg.Topic, ClientID: "tack-audit-relay", ProduceTimeout: 0,
	})
	if err != nil {
		pool.Close()
		slog.ErrorContext(ctx, "audit_consumer.relay_recorder_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("new relay recorder: %w", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(pool), FoundationDB: relayFoundationDB,
		PollInterval: cfg.PollInterval, BatchSize: cfg.BatchSize, AfterProduce: cfg.AfterProduce,
		AfterLeaseAcquired: cfg.AfterLeaseAcquired,
	})
	if err != nil {
		pool.Close()
		if closeErr := recorder.CloseContext(ctx); closeErr != nil {
			slog.ErrorContext(ctx, "audit_consumer.relay_recorder_close_failed", slog.String("err", closeErr.Error()))
		}
		slog.ErrorContext(ctx, "audit_consumer.relay_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("new audit relay: %w", err)
	}
	return Opened{Relay: relay, Pool: pool, Recorder: recorder}, nil
}
