package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditReconstructReferenceRenamesInput struct {
	clispec.InputMarker
}

type auditReconstructReferenceRenamesResult struct {
	clispec.ResultMarker
	Command string                    `json:"command"`
	DryRun  bool                      `json:"dry_run"`
	Count   int                       `json:"count"`
	Renames []referenceRenameEvidence `json:"renames"`
}

func auditReconstructReferenceRenamesOp(
	factory *cli.Factory,
) clispec.Operation[auditReconstructReferenceRenamesInput] {
	return clispec.Operation[auditReconstructReferenceRenamesInput]{
		Name: clispec.Name{Canonical: "reconstruct-reference-renames", CLIOverride: ""},
		Audit: audit.Spec{
			Verb:    string(audit.VerbOpsAuditReconstructReferenceRenames),
			Mutates: true,
		},
		Group: auditOpsGroup,
		Short: "Reconstruct the TACK-342 reference rename audit history",
		Long: "Defaults to a dry run that prints the 104 cited reference renames. " +
			"Pass --execute to append reconstructed events with current ledger times.",
		New: func() auditReconstructReferenceRenamesInput {
			return auditReconstructReferenceRenamesInput{InputMarker: clispec.InputMarker{}}
		},
		DryRun: func(
			ctx context.Context,
			_ auditReconstructReferenceRenamesInput,
			sink clispec.ResultSink,
		) error {
			return writeReferenceRenameBackfillPreview(ctx, sink)
		},
		Run: func(
			ctx context.Context,
			_ auditReconstructReferenceRenamesInput,
			sink clispec.ResultSink,
		) error {
			return runReferenceRenameBackfill(ctx, factory, sink)
		},
	}
}

func writeReferenceRenameBackfillPreview(ctx context.Context, sink clispec.ResultSink) error {
	renames, err := loadReferenceRenameEvidence()
	if err != nil {
		return err
	}
	if err := clispec.WriteJSONValue(ctx, sink, auditReconstructReferenceRenamesResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.audit.reconstruct-reference-renames",
		DryRun:       true,
		Count:        len(renames),
		Renames:      renames,
	}); err != nil {
		wrapped := fmt.Errorf("write reference rename reconstruction preview: %w", err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.preview_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

func runReferenceRenameBackfill(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
) error {
	if err := reconstructReferenceRenameEvents(ctx, factory.AuditOutbox(), factory.Cfg); err != nil {
		return err
	}
	renames, err := loadReferenceRenameEvidence()
	if err != nil {
		return err
	}
	if err := clispec.WriteJSONValue(ctx, sink, auditReconstructReferenceRenamesResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.audit.reconstruct-reference-renames",
		DryRun:       false,
		Count:        len(renames),
		Renames:      renames,
	}); err != nil {
		wrapped := fmt.Errorf("write reference rename reconstruction result: %w", err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.result_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
