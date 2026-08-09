package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

type auditReconstructReferenceRepairInput struct {
	clispec.InputMarker
}

type auditReconstructReferenceRepairResult struct {
	clispec.ResultMarker
	Command string               `json:"command"`
	DryRun  bool                 `json:"dry_run"`
	Counts  []auditBackfillCount `json:"counts"`
}

func auditReconstructReferenceRepairOp(
	factory *cli.Factory,
) clispec.Operation[auditReconstructReferenceRepairInput] {
	// The command name and audit verb retain their ledger names because existing
	// ledger rows use them. Renaming either would split one operation's record.
	return clispec.Operation[auditReconstructReferenceRepairInput]{
		Name: clispec.Name{Canonical: "reconstruct-reference-renames", CLIOverride: ""},
		Audit: audit.Spec{
			Verb:    string(audit.VerbOpsAuditReconstructReferenceRenames),
			Mutates: true,
		},
		Group: auditOpsGroup,
		Short: "Reconstruct the TACK-342 reference repair and seed audit history",
		Long: "Defaults to a dry run that derives and reports every reconstruction class. " +
			"Pass --execute to append only count-verified events with current ledger times.",
		New: func() auditReconstructReferenceRepairInput {
			return auditReconstructReferenceRepairInput{InputMarker: clispec.InputMarker{}}
		},
		DryRun: func(
			ctx context.Context,
			_ auditReconstructReferenceRepairInput,
			sink clispec.ResultSink,
		) error {
			return writeReferenceRepairBackfillPreview(ctx, factory, sink)
		},
		Run: func(
			ctx context.Context,
			_ auditReconstructReferenceRepairInput,
			sink clispec.ResultSink,
		) error {
			return runReferenceRepairBackfill(ctx, factory, sink)
		},
	}
}

func writeReferenceRepairBackfillPreview(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
) error {
	return writeReferenceRepairBackfillPreviewWith(
		ctx,
		factory,
		sink,
		previewReferenceRepairHistory,
	)
}

type referenceRepairHistoryPreviewer func(
	context.Context,
	*config.Config,
	audit.OperatorPrincipal,
) (auditBackfillPlan, error)

func writeReferenceRepairBackfillPreviewWith(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
	preview referenceRepairHistoryPreviewer,
) error {
	principal, err := factory.OperatorIdentitySource().Resolve(ctx)
	if err != nil {
		wrapped := fmt.Errorf("resolve operator for reference repair reconstruction preview: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_repair_backfill.preview_principal_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	plan, err := preview(ctx, factory.Cfg, principal)
	if err != nil {
		return err
	}
	return writeReferenceRepairBackfillReport(ctx, sink, true, plan)
}

func writeReferenceRepairBackfillReport(
	ctx context.Context,
	sink clispec.ResultSink,
	dryRun bool,
	plan auditBackfillPlan,
) error {
	if err := clispec.WriteJSONValue(ctx, sink, auditReconstructReferenceRepairResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.audit.reconstruct-reference-renames",
		DryRun:       dryRun,
		Counts:       plan.Counts(),
	}); err != nil {
		wrapped := fmt.Errorf("write reference repair reconstruction report: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_repair_backfill.report_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

func runReferenceRepairBackfill(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
) error {
	plan, err := reconstructReferenceRepairEvents(ctx, factory.AuditOutbox(), factory.Cfg)
	if err != nil {
		return err
	}
	return writeReferenceRepairBackfillReport(ctx, sink, false, plan)
}
