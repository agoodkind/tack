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

type killtestSchemaOutput struct {
	clispec.ResultMarker
	Command string               `json:"command"`
	DryRun  bool                 `json:"dry_run"`
	Result  KilltestSchemaResult `json:"result"`
}

func killtestSchemaBackfillOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "drop-killtest-schema", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-560", RemoveBy: time.Date(2026, time.December, 6, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillDropKilltestSchema), Mutates: true},
		Group:    opsGroup,
		Short:    "Drop the killtest schema and its tables from the ledger database",
		Long: "The command reads the DATABASE_URL login, killtest ownership, and table row counts. " +
			"The command reports an absent killtest schema and exits 0. " +
			"The command exits with an error and drops no object when the login is neither a superuser " +
			"nor a member of the owning role, killtest contains a view, function, type, or sequence no " +
			"table owns, or audit, partman, or public objects depend on killtest objects. " +
			"The command reports the schema and tables. The command changes no object without --execute. " +
			"The command runs DROP SCHEMA killtest CASCADE and reports dropped tables with --execute.",
		New: func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runKilltestSchemaCommand(ctx, f, sink, true)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runKilltestSchemaCommand(ctx, f, sink, false)
		},
	}
}

func runKilltestSchemaCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, dryRun bool) error {
	pool, err := postgres.NewPool(ctx, f.Cfg.DatabaseURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "killtest_schema.connect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("connect to the ledger: %w", err)
	}
	defer pool.Close()
	result, err := RunDropKilltestSchema(ctx, pool, dryRun)
	if err != nil {
		return err
	}
	output := killtestSchemaOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.backfill.once-drop-killtest-schema",
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		slog.ErrorContext(ctx, "killtest_schema.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the killtest schema result: %w", err)
	}
	return nil
}
