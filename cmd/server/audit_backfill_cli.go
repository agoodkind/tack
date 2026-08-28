package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditBackfillOrgInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
}

// auditBackfillOrgOp declares `audit backfill-org`: the one-time TACK-461
// move of nil-org ledger rows onto the deployment's sole org. The command
// takes no target flag on purpose: it derives the org from org_members and
// refuses unless exactly one exists and the ledger names no other, so a
// multi-org deployment can never reach the rewrite. Without --execute it
// reports the derived target and row counts and changes nothing.
func auditBackfillOrgOp(f *cli.Factory) clispec.Operation[auditBackfillOrgInput] {
	return clispec.Operation[auditBackfillOrgInput]{
		Name:    clispec.Name{Canonical: "backfill-org", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbAuditOrgBackfilled), Mutates: true},
		Group:   auditGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Move nil-org ledger rows onto the deployment's sole org",
		Long: "Rewrites every audit.events row recorded under the all-zero org onto the " +
			"sole org's hash chains: existing rows keep their seq and hashes, each moved " +
			"row is appended with a recomputed hash, and the nil org's chain heads are " +
			"removed. Refuses unless org_members holds exactly one org and the ledger " +
			"names no other. Connects through DATABASE_URL, because the rewrite needs " +
			"the owning role.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New: func() auditBackfillOrgInput {
			return auditBackfillOrgInput{InputMarker: clispec.InputMarker{}}
		},
		DryRun: func(ctx context.Context, in auditBackfillOrgInput, sink clispec.ResultSink) error {
			return runAuditBackfillOrg(ctx, f, sink, false)
		},
		Run: func(ctx context.Context, in auditBackfillOrgInput, sink clispec.ResultSink) error {
			return runAuditBackfillOrg(ctx, f, sink, true)
		},
	}
}

func runAuditBackfillOrg(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, apply bool) error {
	backfill, err := audit.NewOrgBackfill(ctx, f.Cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: open: %w", err)
	}
	defer backfill.Close()

	target, err := deriveBackfillTarget(ctx, backfill.Pool())
	if err != nil {
		return err
	}
	plan, err := backfill.PlanNilOrgMove(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_plan_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: plan: %w", err)
	}
	result := auditBackfillOrgResult{
		Command: "audit.backfill_org", DryRun: !apply, TargetOrg: target,
		NilRows: plan.NilRows, Shards: plan.Shards, RowsMoved: 0, ShardsTouched: 0, Passes: 0,
	}
	if apply {
		move, err := backfill.MoveNilOrgRows(ctx, target, soleOrgGuard(target))
		if err != nil {
			slog.ErrorContext(ctx, "audit.backfill_move_failed",
				slog.String("target_org", target.String()), slog.String("err", err.Error()))
			return fmt.Errorf("audit backfill-org: move: %w", err)
		}
		result.RowsMoved = move.RowsMoved
		result.ShardsTouched = move.ShardsTouched
		result.Passes = move.Passes
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "audit.backfill_render_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: render report: %w", err)
	}
	return nil
}

// deriveBackfillTarget resolves the deployment's sole org and refuses when
// the sole-org premise does not hold: the mapping "every nil row belongs to
// this org" is only provable when no other org has ever existed here.
func deriveBackfillTarget(ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, error) {
	memberOrgs, err := distinctUUIDs(ctx, pool, `SELECT DISTINCT org_id FROM org_members`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_member_orgs_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("audit backfill-org: list member orgs: %w", err)
	}
	if len(memberOrgs) != 1 {
		return uuid.Nil, fmt.Errorf("audit backfill-org: org_members holds %d orgs, need exactly 1", len(memberOrgs))
	}
	target := memberOrgs[0]
	ledgerOrgs, err := distinctUUIDs(ctx, pool,
		`SELECT DISTINCT org_id FROM audit.events WHERE org_id <> '00000000-0000-0000-0000-000000000000'`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_ledger_orgs_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("audit backfill-org: list ledger orgs: %w", err)
	}
	for _, org := range ledgerOrgs {
		// Operator commands record under the reserved system org, never a
		// customer org and never nil, so its rows neither weaken the sole-org
		// premise nor belong to the move.
		if org == audit.SystemOrgID() {
			continue
		}
		if org != target {
			return uuid.Nil, fmt.Errorf("audit backfill-org: ledger names org %s besides the sole member org %s", org, target)
		}
	}
	return target, nil
}

// soleOrgGuard re-asserts the sole-org premise inside every chunk
// transaction, so a membership created mid-run aborts the move before the
// next chunk commits instead of silently attributing new history to the
// wrong org.
func soleOrgGuard(target uuid.UUID) audit.ShardGuard {
	return func(ctx context.Context, tx pgx.Tx) error {
		orgs, err := collectUUIDs(tx.Query(ctx, `SELECT DISTINCT org_id FROM org_members`))
		if err != nil {
			slog.ErrorContext(ctx, "audit.backfill_guard_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit backfill-org: premise guard: %w", err)
		}
		if len(orgs) != 1 || orgs[0] != target {
			return fmt.Errorf("audit backfill-org: sole-org premise broke mid-run: org_members holds %d orgs", len(orgs))
		}
		return nil
	}
}

func distinctUUIDs(ctx context.Context, pool *pgxpool.Pool, query string) ([]uuid.UUID, error) {
	return collectUUIDs(pool.Query(ctx, query))
}

// collectUUIDs drains a single-UUID-column query result. It takes the query
// call's pair directly so pool-backed and transaction-backed queries share
// one path.
func collectUUIDs(rows pgx.Rows, queryErr error) ([]uuid.UUID, error) {
	if queryErr != nil {
		slog.Error("audit.backfill_distinct_query_failed", slog.String("err", queryErr.Error()))
		return nil, fmt.Errorf("distinct org query: %w", queryErr)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			slog.Error("audit.backfill_distinct_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("distinct org scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("audit.backfill_distinct_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("distinct org rows: %w", err)
	}
	return out, nil
}
