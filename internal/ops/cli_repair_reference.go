package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

type repairReferenceUniquenessInput struct {
	clispec.InputMarker
	Keep string
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
		Long: "Prints every planned rename and changes nothing until --execute is " +
			"passed. Renumbering changes a reference a person may have written " +
			"down, so read the printed mapping first.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[repairReferenceUniquenessInput]{
			clispec.StringParam("keep", "which node keeps a contested reference: oldest or newest",
				keepOldest, false,
				func(input *repairReferenceUniquenessInput, value string) { input.Keep = value }),
		},
		New: func() repairReferenceUniquenessInput {
			return repairReferenceUniquenessInput{
				InputMarker: clispec.InputMarker{},
				Keep:        keepOldest,
			}
		},
		// The global --execute is the only action gate. Declaring a second flag
		// of that name here would take the name over, leave the audit
		// choke-point reading false, and make the command unable to apply
		// anything (TACK-449).
		DryRun: func(
			ctx context.Context,
			input repairReferenceUniquenessInput,
			sink clispec.ResultSink,
		) error {
			return runRepairReferenceUniquenessCommand(ctx, f, input, sink, false)
		},
		Run: func(
			ctx context.Context,
			input repairReferenceUniquenessInput,
			sink clispec.ResultSink,
		) error {
			return runRepairReferenceUniquenessCommand(ctx, f, input, sink, true)
		},
	}
}

func runRepairReferenceUniquenessCommand(
	ctx context.Context,
	factory *cli.Factory,
	input repairReferenceUniquenessInput,
	sink clispec.ResultSink,
	execute bool,
) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for the reference repair: %w", err)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.env_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()

	report, err := RepairReferenceUniqueness(ctx, env, RepairReferenceOptions{
		Execute: execute,
		Keep:    input.Keep,
	})
	if err != nil {
		wrapped := fmt.Errorf("repair reference uniqueness: %w", err)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	// The ledger records what the repair did only after it did it, so a run
	// that fails partway records the part that landed and nothing more.
	if execute {
		if err := recordRepairReferenceRun(ctx, factory, report); err != nil {
			return err
		}
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
		DryRun:         !execute,
		Keep:           input.Keep,
		CountersSeeded: len(report.Counters),
		KeysWritten:    len(report.Keys),
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

// recordRepairReferenceRun puts one ledger row behind every reference the run
// changed, every counter it seeded, and every key it claimed.
func recordRepairReferenceRun(
	ctx context.Context,
	factory *cli.Factory,
	report RepairReferenceReport,
) error {
	principal, err := factory.OperatorIdentitySource().Resolve(ctx)
	if err != nil {
		wrapped := fmt.Errorf("resolve operator for the reference repair record: %w", err)
		slog.ErrorContext(ctx, "repair.reference_uniqueness.principal_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return recordReferenceRepair(ctx, factory.AuditOutbox(), principal, report, clock.Now().UTC())
}
