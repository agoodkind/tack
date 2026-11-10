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

// partitionHeadroomAlertFloor equals the pg_partman premake count that
// migration 014 sets for audit.events. Successful maintenance creates weeks
// until the newest partition starts at least two weeks after now.
// HeadroomWeeks counts only partitions that start after now. A freshly
// provisioned ledger returned 3 after a midweek run. The manager logs an alert
// when the count falls below this floor.
const partitionHeadroomAlertFloor = 2

// partitionStore defines database operations for the partition manager.
// The interface permits maintenance-loop tests without a database.
type partitionStore interface {
	// RunMaintenance creates future partitions for audit.events.
	RunMaintenance(ctx context.Context) error
	// HeadroomWeeks counts weekly partitions that start after now. The count
	// represents the remaining future partition buffer.
	HeadroomWeeks(ctx context.Context, now time.Time) (int, error)
	// StrayChildren returns the children of audit.events named outside
	// events_pYYYY_MM_DD.
	StrayChildren(ctx context.Context) ([]string, error)
}

// PartitionManager maintains future weekly partitions for audit.events.
// Like Notarizer, it uses a goroutine with panic recovery and runs at startup
// to create missed partitions, then repeats at a fixed period. The store uses
// the consumer's shared pool. Close does not close that pool.
type PartitionManager struct {
	store     partitionStore
	period    time.Duration
	recording *partitionRecording

	stop    chan struct{}
	stopped chan struct{}
}

// NewPartitionManager constructs a manager using store.
//
// The period controls timer ticks after the startup maintenance run.
//
//   - A positive period sets the maintenance interval.
//   - A zero or negative period selects a 24h interval.
func NewPartitionManager(store partitionStore, period time.Duration) *PartitionManager {
	if period <= 0 {
		period = 24 * time.Hour
	}

	return &PartitionManager{
		store:     store,
		period:    period,
		recording: nil,
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

// Start starts the maintenance loop in a goroutine with panic recovery.
//
// The loop runs maintenance before waiting for its first timer tick.
//
//   - A new consumer attempts maintenance at startup.
//   - Later timer ticks repeat maintenance at the configured interval.
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
	// The startup maintenance run establishes headroom for a fresh or restarted
	// consumer and creates missed weeks before the first tick.
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
	m.reportStrayChildren(ctx)
	maintained := true
	if err := m.maintain(ctx); err != nil {
		maintained = false
		telemetry.IncAuditPartitionMaintenance("error")
		telemetry.L(ctx).Error(
			"audit.partition.maintenance_failed",
			slog.String("err", err.Error()),
		)
	} else {
		telemetry.IncAuditPartitionMaintenance("ok")
	}

	// The manager reads headroom even after maintenance fails. Repeated failures
	// do not create partitions. The gauge and low-headroom alert report the
	// remaining future weeks alongside the maintenance error counter and log.
	headroom, err := m.store.HeadroomWeeks(ctx, clock.Now().UTC())
	if err != nil {
		telemetry.L(ctx).Error(
			"audit.partition.headroom_query_failed",
			slog.String("err", err.Error()),
		)

		return
	}

	telemetry.SetAuditPartitionHeadroomWeeks(int64(headroom))
	if headroom < partitionHeadroomAlertFloor {
		telemetry.L(ctx).Error(
			"audit.partition.headroom_low",
			slog.Int("headroom_weeks", headroom),
			slog.Int("floor_weeks", partitionHeadroomAlertFloor),
		)

		return
	}

	if maintained {
		telemetry.L(ctx).Debug("audit.partition.maintained", slog.Int("headroom_weeks", headroom))
	}
}

// Close stops the maintenance loop without closing the shared pool.
//
// The method waits for the loop to stop and permits repeated calls.
//
//   - A second Close call returns after the loop has stopped.
func (m *PartitionManager) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}

	<-m.stopped

	return nil
}

// pgPartitionStore implements partitionStore using the consumer's YugabyteDB
// pool and audit_writer role. Maintenance calls the SECURITY DEFINER wrapper
// that migration 004 installs.
type pgPartitionStore struct {
	pool *pgxpool.Pool
}

// NewPGPartitionStore constructs the production partition store using pool.
//
// The store uses the supplied pool for maintenance and partition queries.
//
//   - Sharing the consumer's pool lets maintenance use its audit_writer role.
func NewPGPartitionStore(pool *pgxpool.Pool) partitionStore {
	return &pgPartitionStore{pool: pool}
}

func (s *pgPartitionStore) HeadroomWeeks(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(
		ctx,
		`
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		AND (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz > $1
	`,
		now,
	).Scan(
		&count,
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.partition.headroom_query_failed",
			slog.String("err", err.Error()),
		)

		return 0, fmt.Errorf("headroom query: %w", err)
	}

	return count, nil
}
