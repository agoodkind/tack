package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditPartitionNamesOutput struct {
	clispec.ResultMarker
	Command string                    `json:"command"`
	DryRun  bool                      `json:"dry_run"`
	Result  AuditPartitionNamesResult `json:"result"`
}

func auditPartitionNamesBackfillOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "audit-partition-names", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-551", RemoveBy: time.Date(2026, time.November, 30, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillAuditPartitionNames), Mutates: true},
		Group:    opsGroup,
		Short:    "Rename each audit.events child outside events_pYYYY_MM_DD to the name of its week",
		Long: "pg_partman reads the text after the last p separator of each audit.events child name as a date, " +
			"and one child named outside events_pYYYY_MM_DD fails every partition maintenance run. " +
			"The command lists each such child. A child that covers one week from Monday 00:00 UTC gets the name " +
			"events_p<that Monday> and a matching primary key name. Any other such child, or a target name that " +
			"exists, refuses the run before any rename. A dry run lists the renames. A second run lists none.",
		New: func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runAuditPartitionNamesCommand(ctx, f, sink, true)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runAuditPartitionNamesCommand(ctx, f, sink, false)
		},
	}
}

func runAuditPartitionNamesCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, dryRun bool) error {
	pool, err := postgres.NewPool(ctx, f.Cfg.DatabaseURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.connect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("connect to the ledger: %w", err)
	}
	defer pool.Close()
	result, err := RunAuditPartitionNamesBackfill(ctx, pool, dryRun)
	if err != nil {
		return err
	}
	output := auditPartitionNamesOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.backfill.once-audit-partition-names",
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the audit partition names result: %w", err)
	}
	return nil
}
