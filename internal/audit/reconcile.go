package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

const (
	defaultReconcilePeriod = 30 * time.Minute
	defaultReconcileWindow = 24 * time.Hour
	reconcileInsertBatch   = 500
)

// Reconciler periodically re-projects audit.events rows that are missing from
// ClickHouse audit.events_olap, converging the analytical read tier back to
// complete after a ClickHouse outage dropped best-effort writes (TACK-316). The
// canonical store is always Yugabyte audit.events; this only repairs the OLAP
// projection, and runs inside the audit-consumer because that process already
// holds both the Yugabyte pool and the ClickHouse connection on the bridge.
type Reconciler struct {
	ybpool  *pgxpool.Pool
	ch      chdriver.Conn
	period  time.Duration
	window  time.Duration
	stop    chan struct{}
	stopped chan struct{}
}

// NewReconciler builds a reconciler over the canonical Yugabyte pool and the
// ClickHouse connection. A non-positive period or window falls back to the
// 30-minute / 24-hour defaults.
func NewReconciler(ybpool *pgxpool.Pool, ch chdriver.Conn, period, window time.Duration) *Reconciler {
	if period <= 0 {
		period = defaultReconcilePeriod
	}
	if window <= 0 {
		window = defaultReconcileWindow
	}
	return &Reconciler{
		ybpool:  ybpool,
		ch:      ch,
		period:  period,
		window:  window,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start launches the reconcile loop in a recovered goroutine.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(ctx, "audit.reconcile.panic", slog.Any("err", rec))
			}
		}()
		r.loop(ctx)
	}()
}

func (r *Reconciler) loop(ctx context.Context) {
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.reconcile"))
	defer close(r.stopped)
	t := time.NewTicker(r.period)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			r.runOnce(ctx)
		}
	}
}

// Close stops the loop. Idempotent.
func (r *Reconciler) Close() error {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	<-r.stopped
	return nil
}

// runOnce repairs the OLAP tier for the recent window: every audit.events row
// whose event_id is absent from audit.events_olap is re-projected.
func (r *Reconciler) runOnce(ctx context.Context) {
	since := clock.Now().Add(-r.window).UTC()
	present, err := r.clickHouseIDs(ctx, since)
	if err != nil {
		return
	}
	missing, err := r.missingFromYugabyte(ctx, since, present)
	if err != nil {
		return
	}
	if len(missing) == 0 {
		return
	}
	repaired := 0
	for start := 0; start < len(missing); start += reconcileInsertBatch {
		end := min(start+reconcileInsertBatch, len(missing))
		// insertOLAPBatch logs its own failure at WARN; stop this run there.
		if err := insertOLAPBatch(ctx, r.ch, missing[start:end]); err != nil {
			return
		}
		repaired += end - start
	}
	slog.InfoContext(ctx, "audit.reconcile.repaired", slog.Int("rows", repaired))
}

// clickHouseIDs returns the set of event_ids already present in the OLAP tier
// for the window.
func (r *Reconciler) clickHouseIDs(ctx context.Context, since time.Time) (map[uuid.UUID]struct{}, error) {
	rows, err := r.ch.Query(ctx, `SELECT event_id FROM audit.events_olap WHERE event_time >= ?`, since)
	if err != nil {
		slog.WarnContext(ctx, "audit.reconcile.clickhouse_scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("reconcile clickhouse query: %w", err)
	}
	defer rows.Close()
	present := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			slog.WarnContext(ctx, "audit.reconcile.clickhouse_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("reconcile clickhouse scan: %w", err)
		}
		present[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "audit.reconcile.clickhouse_scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("reconcile clickhouse rows: %w", err)
	}
	return present, nil
}

// missingFromYugabyte scans the canonical rows in the window and returns those
// whose event_id is not already in present.
func (r *Reconciler) missingFromYugabyte(ctx context.Context, since time.Time, present map[uuid.UUID]struct{}) ([]projectedEvent, error) {
	rows, err := r.ybpool.Query(ctx, `
		SELECT org_id, shard, event_time, event_id, seq,
		       actor_id, actor_kind, action, entity_kind, entity_id,
		       context, delta, pii_ref, prev_hash, row_hash, idempotency_key
		  FROM audit.events
		 WHERE event_time >= $1
		 ORDER BY event_time`, since)
	if err != nil {
		slog.WarnContext(ctx, "audit.reconcile.yugabyte_scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("reconcile yugabyte query: %w", err)
	}
	defer rows.Close()
	var missing []projectedEvent
	for rows.Next() {
		var pe projectedEvent
		var pii pgtype.UUID
		if err := rows.Scan(
			&pe.OrgID, &pe.Shard, &pe.EventTime, &pe.EventID, &pe.Seq,
			&pe.ActorID, &pe.ActorKind, &pe.Action, &pe.EntityKind, &pe.EntityID,
			&pe.Context, &pe.Delta, &pii, &pe.PrevHash, &pe.RowHash, &pe.IdemKey,
		); err != nil {
			slog.WarnContext(ctx, "audit.reconcile.yugabyte_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("reconcile yugabyte scan: %w", err)
		}
		if pii.Valid {
			ref := uuid.UUID(pii.Bytes)
			pe.PIIRef = &ref
		}
		if _, ok := present[pe.EventID]; ok {
			continue
		}
		missing = append(missing, pe)
	}
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "audit.reconcile.yugabyte_scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("reconcile yugabyte rows: %w", err)
	}
	return missing, nil
}
