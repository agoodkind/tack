package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type repairReferenceUniquenessInput struct {
	clispec.InputMarker
	Execute bool
	Keep    string
}

// repairReferenceRenameResult is one planned or applied reference change. An
// operator reads these before executing, because a reference is something a
// person may have written down.
type repairReferenceRenameResult struct {
	NodeID string `json:"node_id"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type repairReferenceUniquenessResult struct {
	clispec.ResultMarker
	Command        string                        `json:"command"`
	DryRun         bool                          `json:"dry_run"`
	Keep           string                        `json:"keep"`
	CountersSeeded int                           `json:"counters_seeded"`
	KeysWritten    int                           `json:"keys_written"`
	RenamedCount   int                           `json:"renamed_count"`
	Renamed        []repairReferenceRenameResult `json:"renamed"`
}

func repairReferenceUniquenessOp(f *cli.Factory) clispec.Operation[repairReferenceUniquenessInput] {
	return clispec.Operation[repairReferenceUniquenessInput]{
		Name:    clispec.Name{Canonical: "reference-uniqueness", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsRepairReferenceUniqueness), Mutates: true},
		Group:   repairGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Renumber duplicated references, seed counters, and backfill the uniqueness index",
		Long: "Defaults to a dry run that prints every planned rename and changes " +
			"nothing. Pass --execute to apply. Renumbering changes a reference a " +
			"person may have written down, so read the printed mapping first.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[repairReferenceUniquenessInput]{
			clispec.BoolParam("execute", "apply the repair instead of reporting it", false,
				func(input *repairReferenceUniquenessInput, value bool) { input.Execute = value }),
			clispec.StringParam("keep", "which node keeps a contested reference: oldest or newest",
				keepOldest, false,
				func(input *repairReferenceUniquenessInput, value string) { input.Keep = value }),
		},
		New: func() repairReferenceUniquenessInput {
			return repairReferenceUniquenessInput{
				InputMarker: clispec.InputMarker{},
				Execute:     false,
				Keep:        keepOldest,
			}
		},
		Run: func(
			ctx context.Context,
			input repairReferenceUniquenessInput,
			sink clispec.ResultSink,
		) error {
			return runRepairReferenceUniquenessCommand(ctx, f, input, sink)
		},
	}
}

func runRepairReferenceUniquenessCommand(
	ctx context.Context,
	factory *cli.Factory,
	input repairReferenceUniquenessInput,
	sink clispec.ResultSink,
) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for the reference repair: %w", err)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.env_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()

	report, err := RepairReferenceUniqueness(ctx, env, RepairReferenceOptions{
		Execute: input.Execute,
		Keep:    input.Keep,
	})
	if err != nil {
		wrapped := fmt.Errorf("repair reference uniqueness: %w", err)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	renamed := make([]repairReferenceRenameResult, 0, len(report.Renumbered))
	for _, rename := range report.Renumbered {
		renamed = append(renamed, repairReferenceRenameResult{
			NodeID: rename.NodeID.String(),
			From:   rename.From,
			To:     rename.To,
		})
	}
	writeErr := clispec.WriteJSONValue(ctx, sink, repairReferenceUniquenessResult{
		ResultMarker:   clispec.ResultMarker{},
		Command:        "ops.repair.reference-uniqueness",
		DryRun:         !input.Execute,
		Keep:           input.Keep,
		CountersSeeded: report.CountersSeeded,
		KeysWritten:    report.KeysWritten,
		RenamedCount:   len(renamed),
		Renamed:        renamed,
	})
	if writeErr != nil {
		wrapped := fmt.Errorf("write the reference repair report: %w", writeErr)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.report_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
