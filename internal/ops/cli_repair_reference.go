package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

// repairRecordTimeout bounds the audit write that runs after the repair. The
// write is mandatory and detached from the caller's cancellation, so it needs
// its own deadline: an interrupted command must still end rather than hang on
// an unreachable ledger.
const repairRecordTimeout = 30 * time.Second

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

// repairReferenceUniquenessResult reports what a run changed, or what a dry
// run would change. Every count is a change: a counter already at its scope's
// highest number and a key its node already holds are re-asserted but not
// counted, so a run over repaired data reports zeros and records nothing.
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
	if outcomeErr := repairRecordOutcome(ctx, report, err, execute, func(recordCtx context.Context) error {
		return recordRepairReferenceRun(recordCtx, factory, report)
	}); outcomeErr != nil {
		return outcomeErr
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

// repairRecordOutcome records what the run applied and returns the error the
// operator has to act on, or nil when the run succeeded and was recorded.
//
// The recording happens whether or not the run failed, because a run that
// failed partway has already written its renames, counter seeds, and key
// claims to FoundationDB; returning the error without recording leaves those
// mutations with no ledger row naming them, which is the 2026-08-07 hole in
// miniature (TACK-452). Recording twice is safe: event identities are derived
// from the facts and the outbox writes only if absent, so a later successful
// rerun re-records the same work rather than doubling it.
//
// When both the run and the recording fail, both errors are returned joined.
// The run failure is what an operator acts on, so it must not be replaced; the
// recording failure must not vanish either, because it means applied changes
// are still unrecorded and a rerun cannot rediscover them. Every applied
// change is also logged by identity, so a ledger that was unreachable leaves
// the operator something to reconcile from rather than a count.
//
// The recording runs on a context detached from cancellation. A run that
// failed because the command was interrupted or timed out would otherwise hand
// the same dead context to the audit write, guaranteeing the record fails
// exactly when it matters most.
func repairRecordOutcome(
	ctx context.Context,
	report RepairReferenceReport,
	runErr error,
	execute bool,
	record func(context.Context) error,
) error {
	var recordErr error
	if execute {
		recordCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), repairRecordTimeout)
		recordErr = record(recordCtx)
		cancel()
		if recordErr != nil {
			logUnrecordedRepair(ctx, report, recordErr)
			if runErr == nil {
				return recordErr
			}
		}
	}
	if runErr == nil {
		return nil
	}
	wrapped := fmt.Errorf("repair reference uniqueness: %w", runErr)
	slog.ErrorContext(ctx, "repair.reference_uniqueness.failed",
		slog.Int("renamed_before_failure", len(report.Renumbered)),
		slog.Int("counters_seeded_before_failure", len(report.Counters)),
		slog.Int("keys_written_before_failure", len(report.Keys)),
		slog.String("err", wrapped.Error()))
	if recordErr != nil {
		return errors.Join(wrapped, recordErr)
	}
	return wrapped
}

// logUnrecordedRepair names every change that is applied and unrecorded. A
// count cannot be reconciled against the store later; an identity can.
func logUnrecordedRepair(ctx context.Context, report RepairReferenceReport, recordErr error) {
	slog.ErrorContext(ctx, "repair.reference_uniqueness.partial_record_failed",
		slog.Int("renamed", len(report.Renumbered)),
		slog.Int("counters_seeded", len(report.Counters)),
		slog.Int("keys_written", len(report.Keys)),
		slog.String("err", recordErr.Error()))
	for _, rename := range report.Renumbered {
		slog.ErrorContext(ctx, "repair.reference_uniqueness.unrecorded_rename",
			slog.String("node_id", rename.NodeID.String()),
			slog.String("from", rename.From),
			slog.String("to", rename.To),
			slog.String("err", recordErr.Error()))
	}
	for _, counter := range report.Counters {
		slog.ErrorContext(ctx, "repair.reference_uniqueness.unrecorded_counter",
			slog.String("counter_key", counter.Key),
			slog.Int64("value", counter.Value),
			slog.String("err", recordErr.Error()))
	}
	for _, key := range report.Keys {
		slog.ErrorContext(ctx, "repair.reference_uniqueness.unrecorded_key",
			slog.String("node_id", key.NodeID.String()),
			slog.String("encoded", key.Encoded),
			slog.String("err", recordErr.Error()))
	}
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
