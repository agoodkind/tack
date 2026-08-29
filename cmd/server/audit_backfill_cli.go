package main

import (
	"context"
	"fmt"
	"log/slog"

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
// refuses unless exactly one exists, the ledger names no other, and every
// nil-org actor belongs to it, so a multi-org deployment can never reach the
// rewrite. Without --execute it reports the derived target and row counts
// and changes nothing.
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
			"removed. Refuses unless org_members holds exactly one org, the ledger names " +
			"no other, and every nil-org actor is a member. Connects through DATABASE_URL, " +
			"because the rewrite needs the owning role.",
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

	target, err := backfill.DeriveSoleOrg(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_derive_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: %w", err)
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
		move, err := backfill.MoveNilOrgRows(ctx, target, audit.SoleOrgGuard(target))
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
