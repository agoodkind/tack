package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
	"goodkind.io/tack/migrations"
)

// emptyInput is the input for top-level commands that take no flags or args.
type emptyInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
}

// buildRoot assembles the cobra command tree. The bare command starts the
// server; serve, migrate, seed, the audit bundle family, and the ops
// maintenance family are subcommands rendered from one declarative registry.
func buildRoot(f *cli.Factory) *cobra.Command {
	serve := serveOp(f)
	root := &cobra.Command{
		Use:           "tack",
		Short:         "Tack operator CLI and server",
		Version:       version.Tag(),
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
	}
	root.SetIn(f.In)
	root.SetOut(f.Out)
	root.SetErr(f.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	f.RegisterGlobalFlags(root)
	f.SetOperatorIdentitySource(cli.NewOperatorSource(f))
	configureAuditOutbox(f)
	configureAuditInfrastructureProbe(f)
	clispec.AttachAudit(root, f, serve.Audit, func(ctx context.Context) error {
		return runServer(ctx, f.Cfg)
	})

	reg := clispec.NewRegistry()
	clispec.Register(reg, serve)
	clispec.Register(reg, migrateOp(f))
	clispec.Register(reg, seedOp(f))
	registerAudit(reg, f)
	ops.RegisterCommands(reg, f)
	for _, command := range clispec.RenderCobra(reg, f) {
		root.AddCommand(command)
	}
	return root
}

const auditInfrastructureProbeTimeout = 5 * time.Second

func configureAuditInfrastructureProbe(f *cli.Factory) {
	databaseURL := f.Cfg.DatabaseURL
	f.SetAuditInfrastructureProbe(func(ctx context.Context) (audit.InfrastructureState, error) {
		probeContext, cancel := context.WithTimeout(ctx, auditInfrastructureProbeTimeout)
		defer cancel()

		connection, err := pgx.Connect(probeContext, databaseURL)
		if err != nil {
			slog.ErrorContext(probeContext, "audit.infrastructure_probe_connect_failed",
				slog.String("err", err.Error()))
			return audit.InfrastructureState{}, fmt.Errorf("connect audit infrastructure probe: %w", err)
		}
		defer func() { connection.Close(probeContext) }()

		var infrastructure audit.InfrastructureState
		if err := connection.QueryRow(
			probeContext,
			"SELECT to_regclass('public.ops_outbox') IS NOT NULL, EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_audit_operator')",
		).Scan(&infrastructure.OutboxTableExists, &infrastructure.OperatorLoginExists); err != nil {
			slog.ErrorContext(probeContext, "audit.infrastructure_probe_query_failed",
				slog.String("err", err.Error()))
			return audit.InfrastructureState{}, fmt.Errorf("query audit infrastructure probe: %w", err)
		}
		return infrastructure, nil
	})
}

func configureAuditOutbox(f *cli.Factory) {
	if f.Cfg.AuditOperatorDSN == "" {
		return
	}

	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(f.Cfg.AuditOperatorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.operator_outbox.config_failed", slog.String("err", err.Error()))
		return
	}
	poolConfig.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		slog.ErrorContext(ctx, "audit.operator_outbox.open_failed", slog.String("err", err.Error()))
		return
	}
	if err := pool.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "audit.operator_outbox.ping_deferred", slog.String("err", err.Error()))
	}
	f.SetAuditOutbox(audit.NewPoolOutbox(pool))
}

func serveOp(f *cli.Factory) clispec.Operation[emptyInput] {
	return clispec.Operation[emptyInput]{
		Name:     clispec.Name{Canonical: "serve"},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbServerServe), Mutates: true},
		Short:    "Start the HTTP server (the default action with no subcommand)",
		New:      func() emptyInput { return emptyInput{} },
		Run: func(ctx context.Context, _ emptyInput, _ clispec.ResultSink) error {
			return runServer(ctx, f.Cfg)
		},
	}
}

// migrateInput contains the inputs for a migration run. A zero flag value does
// not set a requirement.
type migrateInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	To                  int
	ExpectFrom          int
}

func migrateOp(f *cli.Factory) clispec.Operation[migrateInput] {
	return clispec.Operation[migrateInput]{
		Name:     clispec.Name{Canonical: "migrate"},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbDatabaseMigrate), Mutates: true},
		Short:    "Run database migrations against DATABASE_URL; without --execute, print the migration state",
		Params: []clispec.Param[migrateInput]{
			clispec.IntParam("to", "apply migrations through this version only", 0,
				func(in *migrateInput, value int) { in.To = value }),
			clispec.IntParam("expect-from", "refuse the run unless the database is at this migration version", 0,
				func(in *migrateInput, value int) { in.ExpectFrom = value }),
		},
		New: func() migrateInput { return migrateInput{To: 0, ExpectFrom: 0} },
		DryRun: func(ctx context.Context, _ migrateInput, sink clispec.ResultSink) error {
			status, err := schemastatus.Read(ctx, f.Cfg.DatabaseURL, migrations.FS)
			if err != nil {
				slog.ErrorContext(ctx, "migrate.status_failed", slog.String("err", err.Error()))
				return fmt.Errorf("migrate: read the migration state: %w", err)
			}
			if f.OutputFormat() != cli.FormatJSON {
				return sink.WriteText(ctx, status.Summary())
			}
			body, err := json.Marshal(status)
			if err != nil {
				slog.ErrorContext(ctx, "migrate.status_encode_failed", slog.String("err", err.Error()))
				return fmt.Errorf("migrate: encode the migration state at version %d: %w", status.AppliedVersion, err)
			}
			return sink.WriteJSON(ctx, body)
		},
		Run: func(ctx context.Context, in migrateInput, sink clispec.ResultSink) error {
			guard := schemastatus.Guard{ExpectFrom: int64(in.ExpectFrom), To: int64(in.To)}
			applied, err := schemastatus.Apply(ctx, f.Cfg.DatabaseURL, migrations.FS, guard)
			if err != nil {
				slog.ErrorContext(ctx, "migrate.failed", slog.String("err", err.Error()))
				return fmt.Errorf("migrate: %w", err)
			}
			return sink.WriteText(ctx, fmt.Sprintf("migrations complete through version %d", applied))
		},
	}
}
