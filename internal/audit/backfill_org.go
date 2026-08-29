package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/telemetry"
)

// OrgBackfill moves ledger rows recorded under the nil org, or under an
// explicitly absorbed org, onto one real org's hash chains. It exists for the
// one-time TACK-461/TACK-462 repair: auth events recorded before the
// middleware stamped orgs carry uuid.Nil, and the org-scoped read surface
// cannot reach them. Moving a row rewrites org_id, seq, prev_hash, and
// row_hash, which row-level security denies to every audit role by design, so
// this connects through the database-owner DSN and runs only as an operator
// command behind the execute gate.
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

// OrgBackfillOptions carries the TACK-462 operator exemptions. Each entry is
// an explicit UUID the operator reviewed by hand; nothing here widens the
// premise on its own.
type OrgBackfillOptions struct {
	// AbsorbOrgs are ledger orgs whose rows move onto the target wholesale.
	// An absorbed org is exempt from the foreign-org premise refusal because
	// the operator has already attributed its history to the target.
	AbsorbOrgs []uuid.UUID
	// AcknowledgedActors are user actors whose nil-org rows move even though
	// they hold no org_members row, because the operator has attributed that
	// history to the target org by hand.
	AcknowledgedActors []uuid.UUID
}

// Validate refuses entries that can never be a reviewed exemption: the nil
// UUID names nothing, and the system org's rows are already exempt and must
// never move.
func (o OrgBackfillOptions) Validate() error {
	if slices.Contains(o.AbsorbOrgs, uuid.Nil) {
		return errors.New("audit org backfill: --absorb-org must not be the nil org; nil rows move by default")
	}
	if slices.Contains(o.AbsorbOrgs, SystemOrgID()) {
		return errors.New("audit org backfill: the system org's operator rows are never absorbed")
	}
	if slices.Contains(o.AcknowledgedActors, uuid.Nil) {
		return errors.New("audit org backfill: --acknowledge-actor must not be the nil actor")
	}
	return nil
}

// acknowledgedSet indexes the acknowledged actors for membership filtering.
func (o OrgBackfillOptions) acknowledgedSet() map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(o.AcknowledgedActors))
	for _, actor := range o.AcknowledgedActors {
		set[actor] = true
	}
	return set
}

// OrgBackfillPlan reports what a move of one source org would touch, without
// touching it.
type OrgBackfillPlan struct {
	// Rows is how many ledger rows carry the source org today.
	Rows int64
	// Shards is how many per-org chain shards hold at least one such row.
	Shards int
}

// PlanOrgMove counts the source org's rows a move would rewrite.
func (b *OrgBackfill) PlanOrgMove(ctx context.Context, source uuid.UUID) (OrgBackfillPlan, error) {
	plan := OrgBackfillPlan{Rows: 0, Shards: 0}
	if b == nil || b.pool == nil {
		return plan, errors.New("audit org backfill not configured")
	}
	rows, err := b.pool.Query(ctx, `
		SELECT shard, count(*) FROM audit.events
		WHERE org_id = $1
		GROUP BY shard
	`, source)
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
		plan.Rows += count
		plan.Shards++
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.plan_rows_failed", slog.String("err", err.Error()))
		return plan, fmt.Errorf("audit org backfill plan rows: %w", err)
	}
	return plan, nil
}
