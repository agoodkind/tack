package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// partitionHeadroomAlertFloor is the number of future weekly partitions below
// which the manager logs an alert. Twelve are premade; four weeks of warning is
// ample lead time to react before audit.events runs out.
const partitionHeadroomAlertFloor = 4

// partitionStore is the data-access seam for the partition-manager, so the loop
// is unit-testable without a database.
type partitionStore interface {
	// RunMaintenance premakes forward partitions for audit.events.
	RunMaintenance(ctx context.Context) error
	// HeadroomWeeks returns the count of weekly partitions whose range starts
	// after now, i.e. the forward buffer remaining.
	HeadroomWeeks(ctx context.Context, now time.Time) (int, error)
}

// PartitionManager keeps audit.events supplied with future weekly partitions.
// It mirrors Notarizer and Reconciler: a recovered goroutine that runs once at
// boot (catch-up) and then on a fixed period. It reuses the consumer's pool
// through the store, so Close does not close the pool.
type PartitionManager struct {
	store  partitionStore
	period time.Duration

	stop    chan struct{}
	stopped chan struct{}
}

// NewPartitionManager builds a manager over the given store. A non-positive
// period falls back to 24h.
func NewPartitionManager(store partitionStore, period time.Duration) *PartitionManager {
	if period <= 0 {
		period = 24 * time.Hour
	}
	return &PartitionManager{
		store:   store,
		period:  period,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start launches the maintenance loop in a recovered goroutine.
func (m *PartitionManager) Start(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "audit.partition.panic", slog.Any("err", r))
			}
		}()
		m.loop(ctx)
	}()
}

func (m *PartitionManager) loop(ctx context.Context) {
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.partition"))
	defer close(m.stopped)
	t := time.NewTicker(m.period)
	defer t.Stop()
	/* Run once at boot so a freshly deployed or restarted consumer establishes
	   headroom and catches up missed weeks before the first tick. */
	m.runOnce(ctx)
	for {
		select {
		case <-m.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			m.runOnce(ctx)
		}
	}
}

func (m *PartitionManager) runOnce(ctx context.Context) {
	if err := m.store.RunMaintenance(ctx); err != nil {
		telemetry.IncAuditPartitionMaintenance("error")
		telemetry.L(ctx).Error("audit.partition.maintenance_failed", slog.String("err", err.Error()))
		return
	}
	telemetry.IncAuditPartitionMaintenance("ok")

	headroom, err := m.store.HeadroomWeeks(ctx, clock.Now().UTC())
	if err != nil {
		telemetry.L(ctx).Error("audit.partition.headroom_query_failed", slog.String("err", err.Error()))
		return
	}
	telemetry.SetAuditPartitionHeadroomWeeks(int64(headroom))
	if headroom < partitionHeadroomAlertFloor {
		telemetry.L(ctx).Error("audit.partition.headroom_low",
			slog.Int("headroom_weeks", headroom),
			slog.Int("floor_weeks", partitionHeadroomAlertFloor),
		)
		return
	}
	telemetry.L(ctx).Debug("audit.partition.maintained", slog.Int("headroom_weeks", headroom))
}

// Close stops the loop. Idempotent. Does not close the shared pool.
func (m *PartitionManager) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	<-m.stopped
	return nil
}

// pgPartitionStore is the production partitionStore backed by the consumer's
// Yugabyte pool (audit_writer role). Maintenance goes through the SECURITY
// DEFINER wrapper installed by migration 004.
type pgPartitionStore struct {
	pool *pgxpool.Pool
}

// NewPGPartitionStore builds the production store.
func NewPGPartitionStore(pool *pgxpool.Pool) partitionStore {
	return &pgPartitionStore{pool: pool}
}

func (s *pgPartitionStore) RunMaintenance(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		slog.ErrorContext(ctx, "audit.partition.maintenance_query_failed",
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("run partition maintenance: %w", err)
	}
	return nil
}

func (s *pgPartitionStore) HeadroomWeeks(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		AND (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz > $1
	`, now).Scan(&count)
	if err != nil {
		slog.ErrorContext(ctx, "audit.partition.headroom_query_failed",
			slog.String("err", err.Error()),
		)
		return 0, fmt.Errorf("headroom query: %w", err)
	}
	return count, nil
}
