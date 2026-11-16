package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

// repairRecordTimeout limits mandatory audit writes after a repair. Recording
// ignores command cancellation but must end if the ledger remains unavailable.
const repairRecordTimeout = 30 * time.Second

type repairReferenceUniquenessInput struct {
	clispec.InputMarker
	Keep             string
	FailAfterRenames int
}

// repairReferenceRenameResult reports a planned or applied reference change.
// Operators must review the mapping before changing references people may use.
type repairReferenceRenameResult struct {
	NodeID string `json:"node_id"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// repairReferenceUniquenessResult counts actual or planned changes, excluding
// counters already at their maximum and existing claims for the same node.
// Reasserting those values does not require a mutation event.
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
		Name:     clispec.Name{Canonical: "reference-uniqueness", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsRepairReferenceUniqueness), Mutates: true},
		Group:    repairGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Renumber duplicated references, seed counters, and backfill the uniqueness index",
		Long: "Print every planned rename without changing state unless --execute is present. " +
			"Renumbering changes references people may have written down. " +
			"Review the printed mapping before execution.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[repairReferenceUniquenessInput]{
			clispec.StringParam(
				"keep",
				"which node keeps a contested reference: oldest or newest",
				keepOldest,
				false,
				func(input *repairReferenceUniquenessInput, value string) { input.Keep = value },
			),
			clispec.IntParam(
				"fail-after-renames",
				"testbed only: stop after this many renames to test audit recording of partial repairs; "+
					"TACK_DATAGEN_ALLOW_TARGET must be qa or local",
				0,
				func(input *repairReferenceUniquenessInput, value int) { input.FailAfterRenames = value },
			),
		},
		New: func() repairReferenceUniquenessInput {
			return repairReferenceUniquenessInput{
				InputMarker:      clispec.InputMarker{},
				Keep:             keepOldest,
				FailAfterRenames: 0,
			}
		},
		// The audit mechanism reads the global --execute flag. A local flag
		// would override it and prevent execution (TACK-449).
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
	if err := checkRepairFaultAllowed(ctx, factory, input.FailAfterRenames); err != nil {
		return err
	}

	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for the reference repair: %w", err)
		slog.ErrorContext(
			ctx,
			"repair.reference_uniqueness.env_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}
	defer env.Close()

	report, err := RepairReferenceUniqueness(
		ctx,
		env,
		RepairReferenceOptions{
			Execute:          execute,
			Keep:             input.Keep,
			FailAfterRenames: input.FailAfterRenames,
		},
	)
	if outcomeErr := repairRecordOutcome(
		ctx,
		report,
		err,
		execute,
		func(recordCtx context.Context) error {
			return recordRepairReferenceRun(recordCtx, factory, report)
		},
	); outcomeErr != nil {
		return outcomeErr
	}

	renamed := make([]repairReferenceRenameResult, 0, len(report.Renumbered))
	for _, rename := range report.Renumbered {
		renamed = append(
			renamed,
			repairReferenceRenameResult{
				NodeID: rename.NodeID.String(),
				From:   rename.From,
				To:     rename.To,
			},
		)
	}

	writeErr := clispec.WriteJSONValue(
		ctx,
		sink,
		repairReferenceUniquenessResult{
			ResultMarker:   clispec.ResultMarker{},
			Command:        "ops.repair.reference-uniqueness",
			DryRun:         !execute,
			Keep:           input.Keep,
			CountersSeeded: len(report.Counters),
			KeysWritten:    len(report.Keys),
			RenamedCount:   len(renamed),
			Renamed:        renamed,
		},
	)
	if writeErr != nil {
		wrapped := fmt.Errorf("write the reference repair report: %w", writeErr)
		slog.ErrorContext(
			ctx,
			"repair.reference_uniqueness.report_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// checkRepairFaultAllowed permits deliberate partial repairs only on disposable
// QA or local targets. It uses the QA generator's write-approval marker.
func checkRepairFaultAllowed(
	ctx context.Context,
	factory *cli.Factory,
	failAfterRenames int,
) error {
	if failAfterRenames <= 0 {
		return nil
	}

	target := ""
	if factory != nil && factory.Cfg != nil {
		target = factory.Cfg.DatagenAllowTarget
	}

	if target == "qa" || target == "local" {
		return nil
	}

	err := fmt.Errorf(
		"--fail-after-renames is a testbed fault and needs TACK_DATAGEN_ALLOW_TARGET to be qa or local, not %q",
		target,
	)
	slog.ErrorContext(
		ctx,
		"repair.reference_uniqueness.fault_refused",
		slog.String("target", target),
		slog.String("err", err.Error()),
	)

	return err
}

// repairRecordOutcome records applied changes even after execution fails
// (TACK-452). Event identities derive from the changes, and the outbox inserts
// only absent events. Repeated recording does not create duplicate rows.
//
// A combined execution and recording failure returns both errors. Recording
// failures also log every applied identity for reconciliation; a rerun cannot
// rediscover renamed nodes. Recording uses a context independent of command
// cancellation to preserve evidence after interruptions and timeouts.
func repairRecordOutcome(
	ctx context.Context,
	report RepairReferenceReport,
	runErr error,
	execute bool,
	record func(context.Context) error,
) error {
	var recordErr error
	if execute {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), repairRecordTimeout)
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
	slog.ErrorContext(
		ctx,
		"repair.reference_uniqueness.failed",
		slog.Int("renamed_before_failure", len(report.Renumbered)),
		slog.Int("counters_seeded_before_failure", len(report.Counters)),
		slog.Int("keys_written_before_failure", len(report.Keys)),
		slog.String("err", wrapped.Error()),
	)
	if recordErr != nil {
		return errors.Join(wrapped, recordErr)
	}

	return wrapped
}

// unrecordedBatchSize bounds identities per fallback log line, not the total.
// Log every identity because a rerun cannot recover renamed nodes and a
// recording failure prevents writing the command report. Batches avoid the
// cost of a separate log line for every change during a large repair.
const unrecordedBatchSize = 50

// logUnrecordedRepair logs every applied, unrecorded change by identity.
// Reconciliation requires identities rather than counts.
func logUnrecordedRepair(ctx context.Context, report RepairReferenceReport, recordErr error) {
	slog.ErrorContext(
		ctx,
		"repair.reference_uniqueness.partial_record_failed",
		slog.Int("renamed", len(report.Renumbered)),
		slog.Int("counters_seeded", len(report.Counters)),
		slog.Int("keys_written", len(report.Keys)),
		slog.Int("identities_per_line", unrecordedBatchSize),
		slog.String("err", recordErr.Error()),
	)

	renames := make([]string, 0, len(report.Renumbered))
	for _, rename := range report.Renumbered {
		renames = append(renames, rename.NodeID.String()+" "+rename.From+" to "+rename.To)
	}

	logUnrecordedClass(ctx, "renames", renames, recordErr)

	counters := make([]string, 0, len(report.Counters))
	for _, counter := range report.Counters {
		counters = append(counters, counter.Key+"="+strconv.FormatInt(counter.Value, 10))
	}

	logUnrecordedClass(ctx, "counter seeds", counters, recordErr)

	keys := make([]string, 0, len(report.Keys))
	for _, key := range report.Keys {
		keys = append(keys, key.NodeID.String()+" "+key.Encoded)
	}

	logUnrecordedClass(ctx, "reference keys", keys, recordErr)
}

// logUnrecordedClass logs every identity in bounded batches. Each line includes
// its batch position and count to permit detecting truncated output.
func logUnrecordedClass(ctx context.Context, class string, identities []string, recordErr error) {
	total := len(identities)
	if total == 0 {
		return
	}

	batches := (total + unrecordedBatchSize - 1) / unrecordedBatchSize
	for batch := range batches {
		start := batch * unrecordedBatchSize
		end := min(start+unrecordedBatchSize, total)
		slog.ErrorContext(
			ctx,
			"repair.reference_uniqueness.unrecorded",
			slog.String("class", class),
			slog.Int("total", total),
			slog.Int("batch", batch+1),
			slog.Int("batches", batches),
			slog.String("identities", strings.Join(identities[start:end], "; ")),
			slog.String("err", recordErr.Error()),
		)
	}
}

// recordRepairReferenceRun records each reference change, counter seed, and
// key claim.
func recordRepairReferenceRun(
	ctx context.Context,
	factory *cli.Factory,
	report RepairReferenceReport,
) error {
	principal, err := factory.OperatorIdentitySource().Resolve(ctx)
	if err != nil {
		wrapped := fmt.Errorf("resolve operator for the reference repair record: %w", err)
		slog.ErrorContext(
			ctx,
			"repair.reference_uniqueness.principal_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return recordReferenceRepair(ctx, factory.AuditOutbox(), principal, report, clock.Now().UTC())
}
