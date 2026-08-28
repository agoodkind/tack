package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/telemetry"
)

// OrgBackfill moves ledger rows recorded under the nil org onto one real
// org's hash chains. It exists for the one-time TACK-461 repair: auth events
// recorded before the middleware stamped orgs carry uuid.Nil, and the
// org-scoped read surface cannot reach them. Moving a row rewrites org_id,
// seq, prev_hash, and row_hash, which row-level security denies to every
// audit role by design, so this connects through the database-owner DSN and
// runs only as an operator command behind the execute gate.
type OrgBackfill struct {
	pool *pgxpool.Pool
}

// NewOrgBackfill opens a pool against the owning role's DSN.
func NewOrgBackfill(ctx context.Context, dsn string) (*OrgBackfill, error) {
	if dsn == "" {
		return nil, errors.New("audit org backfill: DATABASE_URL required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.pool_config_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill pool config: %w", err)
	}
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.pool_open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill pool open: %w", err)
	}
	return &OrgBackfill{pool: pool}, nil
}

// Close releases the underlying pool. Idempotent.
func (b *OrgBackfill) Close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

// Pool exposes the owner pool so the command layer can run its sole-org
// precondition queries over the same connection the move uses.
func (b *OrgBackfill) Pool() *pgxpool.Pool {
	if b == nil {
		return nil
	}
	return b.pool
}

// OrgBackfillPlan reports what a move would touch, without touching it.
type OrgBackfillPlan struct {
	// NilRows is how many ledger rows carry the nil org today.
	NilRows int64
	// Shards is how many per-org chain shards hold at least one such row.
	Shards int
}

// PlanNilOrgMove counts the nil-org rows a move would rewrite.
func (b *OrgBackfill) PlanNilOrgMove(ctx context.Context) (OrgBackfillPlan, error) {
	plan := OrgBackfillPlan{NilRows: 0, Shards: 0}
	if b == nil || b.pool == nil {
		return plan, errors.New("audit org backfill not configured")
	}
	rows, err := b.pool.Query(ctx, `
		SELECT shard, count(*) FROM audit.events
		WHERE org_id = $1
		GROUP BY shard
	`, uuid.Nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.plan_failed", slog.String("err", err.Error()))
		return plan, fmt.Errorf("audit org backfill plan: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var shard int16
		var count int64
		if err := rows.Scan(&shard, &count); err != nil {
			slog.ErrorContext(ctx, "audit.backfill.plan_scan_failed", slog.String("err", err.Error()))
			return plan, fmt.Errorf("audit org backfill plan scan: %w", err)
		}
		plan.NilRows += count
		plan.Shards++
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.plan_rows_failed", slog.String("err", err.Error()))
		return plan, fmt.Errorf("audit org backfill plan rows: %w", err)
	}
	return plan, nil
}
