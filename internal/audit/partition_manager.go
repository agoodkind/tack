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

// partitionHeadroomAlertFloor sets the minimum future partition count before
// the manager alerts. Maintenance creates weekly partitions using migration
// 014's pg_partman settings; HeadroomWeeks counts starts after the current time.
const partitionHeadroomAlertFloor = 2

// partitionStore defines PartitionManager's database operations.
type partitionStore interface {
	// RunMaintenance creates future partitions for audit.events.
	RunMaintenance(ctx context.Context) error
	// HeadroomWeeks counts weekly partitions starting after now.
	HeadroomWeeks(ctx context.Context, now time.Time) (int, error)
	// StrayChildren returns audit.events children outside events_pYYYY_MM_DD.
	StrayChildren(ctx context.Context) ([]string, error)
}

// PartitionManager creates future weekly audit.events partitions at startup
// and on a fixed interval. It recovers goroutine panics and shares the consumer
// pool without closing that pool during shutdown.
type PartitionManager struct {
	store     partitionStore
	period    time.Duration
	recording *partitionRecording

	stop    chan struct{}
	stopped chan struct{}
}

// NewPartitionManager uses a 24-hour period when period is nonpositive.
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

// Start launches maintenance in a goroutine that recovers panics.
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
	// Create missing future weeks before waiting for the first tick.
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

	// Read future coverage even after maintenance fails. The gauge and alert
	// must report shrinking coverage during repeated failures.
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

// Close stops the loop without closing the shared pool and supports repeated calls.
func (m *PartitionManager) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}

	<-m.stopped

	return nil
}

// pgPartitionStore uses the consumer's audit_writer pool. Maintenance calls
// the SECURITY DEFINER wrapper installed by migration 004.
type pgPartitionStore struct {
	pool *pgxpool.Pool
}

// NewPGPartitionStore builds the production store.
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
