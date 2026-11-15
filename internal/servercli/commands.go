package servercli

import (
	"context"
	_ "embed"
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
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
)

// emptyInput is the input for top-level commands that take no flags or args.
type emptyInput struct {
	clispec.InputMarker
}

// buildRoot assembles the cobra command tree. The bare command starts the
// server; serve, migrate, seed, the audit bundle family, and the ops
// maintenance family are subcommands rendered from one declarative registry.
func buildRoot(factory *cli.Factory) *cobra.Command {
	serve := serveOp(factory)
	root := &cobra.Command{
		Use:           "tack",
		Short:         "Tack operator CLI and server",
		Version:       version.Tag(),
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
	}
	root.SetIn(factory.In)
	root.SetOut(factory.Out)
	root.SetErr(factory.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	factory.RegisterGlobalFlags(root)
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	clispec.AttachAudit(
		root,
		factory,
		serve.Audit,
		func(ctx context.Context) error {
			return runServer(ctx, factory.Cfg)
		},
	)

	reg := clispec.NewRegistry()
	clispec.Register(reg, serve)
	reg.AddHandwritten(clispec.HandwrittenCommand{
		Group: nil,
		Build: migrateCommand,
	})
	clispec.Register(reg, seedOp(factory))
	registerAudit(reg, factory)
	ops.RegisterCommands(reg, factory)

	for _, command := range clispec.RenderCobra(reg, factory) {
		root.AddCommand(command)
	}

	return root
}

const auditInfrastructureProbeTimeout = 5 * time.Second

// auditInfrastructureProbeQuery reports whether public.ops_outbox exists,
// whether the tack_audit_operator login exists, and whether audit_operator
// can execute public.ops_read_begin.
//
//go:embed audit_infrastructure_probe.sql
var auditInfrastructureProbeQuery string

func configureAuditInfrastructureProbe(f *cli.Factory) {
	databaseURL := f.Cfg.DatabaseURL
	f.SetAuditInfrastructureProbe(func(ctx context.Context) (audit.InfrastructureState, error) {
		probeContext, cancel := context.WithTimeout(ctx, auditInfrastructureProbeTimeout)
		defer cancel()

		connection, err := pgx.Connect(probeContext, databaseURL)
		if err != nil {
			slog.ErrorContext(
				probeContext,
				"audit.infrastructure_probe_connect_failed",
				slog.String("err", err.Error()),
			)

			return audit.InfrastructureState{}, fmt.Errorf(
				"connect audit infrastructure probe: %w",
				err,
			)
		}

		defer func() { connection.Close(probeContext) }()

		var infrastructure audit.InfrastructureState
		if err := connection.QueryRow(probeContext, auditInfrastructureProbeQuery).Scan(
			&infrastructure.OutboxTableExists,
			&infrastructure.OperatorLoginExists,
			&infrastructure.OperatorReadsExist,
		); err != nil {
			slog.ErrorContext(
				probeContext,
				"audit.infrastructure_probe_query_failed",
				slog.String("err", err.Error()),
			)

			return audit.InfrastructureState{}, fmt.Errorf(
				"query audit infrastructure probe: %w",
				err,
			)
		}

		return infrastructure, nil
	})
}

func configureAuditOutbox(factory *cli.Factory) {
	if factory.Cfg.AuditOperatorDSN == "" {
		return
	}

	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(factory.Cfg.AuditOperatorDSN)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.operator_outbox.config_failed",
			slog.String("err", err.Error()),
		)

		return
	}

	poolConfig.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		slog.ErrorContext(ctx, "audit.operator_outbox.open_failed", slog.String("err", err.Error()))

		return
	}

	if err := pool.Ping(ctx); err != nil {
		slog.WarnContext(
			ctx,
			"audit.operator_outbox.ping_deferred",
			slog.String("err", err.Error()),
		)
	}

	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
}

func serveOp(factory *cli.Factory) clispec.Operation[emptyInput] {
	return clispec.Operation[emptyInput]{
		Group:    nil,
		Aliases:  nil,
		Hidden:   false,
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		DryRun:   nil,

		Name: clispec.Name{
			CLIOverride: "",
			Canonical:   "serve",
		},
		Lifetime: clispec.Permanent,
		Audit: audit.Spec{
			Atomic:                     false,
			Reads:                      false,
			CreatesAuditInfrastructure: false,
			Class:                      "",
			Targets:                    nil,
			SafeInput:                  nil,
			Verb:                       string(audit.VerbServerServe),
			Mutates:                    true,
		},
		Short: "Start the HTTP server (the default action with no subcommand)",
		New: func() emptyInput {
			return emptyInput{
				InputMarker: clispec.InputMarker{},
			}
		},
		Run: func(ctx context.Context, _ emptyInput, _ clispec.ResultSink) error {
			return runServer(ctx, factory.Cfg)
		},
	}
}
