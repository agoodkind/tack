package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
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
		Name:  clispec.Name{Canonical: "serve"},
		Audit: audit.Spec{Verb: string(audit.VerbServerServe), Mutates: true},
		Short: "Start the HTTP server (the default action with no subcommand)",
		New:   func() emptyInput { return emptyInput{} },
		Run: func(ctx context.Context, _ emptyInput, _ clispec.ResultSink) error {
			return runServer(ctx, f.Cfg)
		},
	}
}

func migrateOp(f *cli.Factory) clispec.Operation[emptyInput] {
	return clispec.Operation[emptyInput]{
		Name:  clispec.Name{Canonical: "migrate"},
		Audit: audit.Spec{Verb: string(audit.VerbDatabaseMigrate), Mutates: true},
		Short: "Run database migrations against DATABASE_URL",
		New:   func() emptyInput { return emptyInput{} },
		Run: func(ctx context.Context, _ emptyInput, sink clispec.ResultSink) error {
			if err := postgres.Migrate(ctx, f.Cfg.DatabaseURL, migrations.FS); err != nil {
				slog.ErrorContext(ctx, "migrate.failed", slog.String("err", err.Error()))
				return fmt.Errorf("migrate: %w", err)
			}
			return sink.WriteText(ctx, "migrations complete")
		},
	}
}
