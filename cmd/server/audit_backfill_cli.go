package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditBackfillOrgInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	AbsorbOrgs          string
	AcknowledgeActors   string
}

// auditBackfillOrgOp declares `audit backfill-org`: the one-time TACK-461
// move of nil-org ledger rows onto the deployment's sole org. The command
// takes no target flag on purpose: it derives the org from org_members and
// refuses unless exactly one exists, the ledger names no other, and every
// nil-org actor belongs to it, so a multi-org deployment can never reach the
// rewrite. The TACK-462 flags are the only exemptions, each an explicit UUID
// the operator reviewed. Without --execute it reports the derived target and
// row counts and changes nothing.
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
			"no other, and every nil-org actor is a member. --absorb-org moves a named " +
			"phantom org's rows the same way instead of refusing on it; " +
			"--acknowledge-actor attributes a named nonmember actor's nil rows by the " +
			"operator's hand. Connects through DATABASE_URL, because the rewrite needs " +
			"the owning role.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditBackfillOrgInput]{
			clispec.StringParam("absorb-org", "comma-separated org UUIDs whose ledger rows move onto the target instead of refusing the premise", "", false,
				func(in *auditBackfillOrgInput, v string) { in.AbsorbOrgs = v }),
			clispec.StringParam("acknowledge-actor", "comma-separated actor UUIDs whose nil-org rows move despite holding no org membership", "", false,
				func(in *auditBackfillOrgInput, v string) { in.AcknowledgeActors = v }),
		},
		New: func() auditBackfillOrgInput {
			return auditBackfillOrgInput{InputMarker: clispec.InputMarker{}, AbsorbOrgs: "", AcknowledgeActors: ""}
		},
		DryRun: func(ctx context.Context, in auditBackfillOrgInput, sink clispec.ResultSink) error {
			return runAuditBackfillOrg(ctx, f, in, sink, false)
		},
		Run: func(ctx context.Context, in auditBackfillOrgInput, sink clispec.ResultSink) error {
			return runAuditBackfillOrg(ctx, f, in, sink, true)
		},
	}
}

// parseUUIDList splits a comma-separated flag value into UUIDs, rejecting
// anything that does not parse so a typo never silently narrows an exemption.
func parseUUIDList(flag, value string) ([]uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, part := range parts {
		id, err := uuid.Parse(strings.TrimSpace(part))
		if err != nil {
			slog.Error("audit.backfill_bad_uuid_flag",
				slog.String("flag", flag), slog.String("value", strings.TrimSpace(part)), slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit backfill-org: --%s %q is not a UUID: %w", flag, strings.TrimSpace(part), err)
		}
		out = append(out, id)
	}
	return out, nil
}

func backfillOptionsFromInput(in auditBackfillOrgInput) (audit.OrgBackfillOptions, error) {
	absorb, err := parseUUIDList("absorb-org", in.AbsorbOrgs)
	if err != nil {
		return audit.OrgBackfillOptions{AbsorbOrgs: nil, AcknowledgedActors: nil}, err
	}
	acked, err := parseUUIDList("acknowledge-actor", in.AcknowledgeActors)
	if err != nil {
		return audit.OrgBackfillOptions{AbsorbOrgs: nil, AcknowledgedActors: nil}, err
	}
	return audit.OrgBackfillOptions{AbsorbOrgs: absorb, AcknowledgedActors: acked}, nil
}

func runAuditBackfillOrg(ctx context.Context, f *cli.Factory, in auditBackfillOrgInput, sink clispec.ResultSink, apply bool) error {
	opts, err := backfillOptionsFromInput(in)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_bad_flags", slog.String("err", err.Error()))
		return err
	}
	backfill, err := audit.NewOrgBackfill(ctx, f.Cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: open: %w", err)
	}
	defer backfill.Close()

	target, err := backfill.DeriveSoleOrg(ctx, opts)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_derive_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: %w", err)
	}
	result, err := planBackfillResult(ctx, backfill, target, opts, apply)
	if err != nil {
		return err
	}
	if apply {
		if err := applyBackfillMoves(ctx, backfill, target, opts, result); err != nil {
			return err
		}
	}
	if err := clispec.WriteJSONValue(ctx, sink, *result); err != nil {
		slog.ErrorContext(ctx, "audit.backfill_render_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: render report: %w", err)
	}
	return nil
}

func planBackfillResult(ctx context.Context, backfill *audit.OrgBackfill, target uuid.UUID, opts audit.OrgBackfillOptions, apply bool) (*auditBackfillOrgResult, error) {
	plan, err := backfill.PlanOrgMove(ctx, uuid.Nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_plan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit backfill-org: plan: %w", err)
	}
	result := &auditBackfillOrgResult{
		Command: "audit.backfill_org", DryRun: !apply, TargetOrg: target,
		NilRows: plan.Rows, Shards: plan.Shards,
		AbsorbedOrgs: nil, AcknowledgedActors: opts.AcknowledgedActors,
		RowsMoved: 0, ShardsTouched: 0, Passes: 0,
	}
	for _, org := range opts.AbsorbOrgs {
		orgPlan, err := backfill.PlanOrgMove(ctx, org)
		if err != nil {
			slog.ErrorContext(ctx, "audit.backfill_plan_failed",
				slog.String("absorb_org", org.String()), slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit backfill-org: plan absorb org %s: %w", org, err)
		}
		result.AbsorbedOrgs = append(result.AbsorbedOrgs, auditBackfillAbsorbedOrg{
			OrgID: org, Rows: orgPlan.Rows, Shards: orgPlan.Shards, RowsMoved: 0,
		})
	}
	return result, nil
}

func applyBackfillMoves(ctx context.Context, backfill *audit.OrgBackfill, target uuid.UUID, opts audit.OrgBackfillOptions, result *auditBackfillOrgResult) error {
	move, err := backfill.MoveOrgRows(ctx, uuid.Nil, target, audit.SoleOrgGuard(target, opts.AcknowledgedActors))
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill_move_failed",
			slog.String("target_org", target.String()), slog.String("err", err.Error()))
		return fmt.Errorf("audit backfill-org: move: %w", err)
	}
	result.RowsMoved = move.RowsMoved
	result.ShardsTouched = move.ShardsTouched
	result.Passes = move.Passes
	for i, org := range opts.AbsorbOrgs {
		absorbMove, err := backfill.MoveOrgRows(ctx, org, target, audit.AbsorbedOrgGuard(target))
		if err != nil {
			slog.ErrorContext(ctx, "audit.backfill_move_failed",
				slog.String("target_org", target.String()),
				slog.String("absorb_org", org.String()), slog.String("err", err.Error()))
			return fmt.Errorf("audit backfill-org: absorb org %s: %w", org, err)
		}
		result.AbsorbedOrgs[i].RowsMoved = absorbMove.RowsMoved
		result.RowsMoved += absorbMove.RowsMoved
	}
	return nil
}
