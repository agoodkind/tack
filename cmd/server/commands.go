package main

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
	clispec.AttachAudit(root, f, serve.Audit, func(ctx context.Context) error {
		return runServer(ctx, f.Cfg)
	})

	reg := clispec.NewRegistry()
	clispec.Register(reg, serve)
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: nil, Build: migrateCommand})
	clispec.Register(reg, seedOp(f))
	registerAudit(reg, f)
	ops.RegisterCommands(reg, f)
	for _, command := range clispec.RenderCobra(reg, f) {
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
			slog.ErrorContext(probeContext, "audit.infrastructure_probe_connect_failed",
				slog.String("err", err.Error()))
			return audit.InfrastructureState{}, fmt.Errorf("connect audit infrastructure probe: %w", err)
		}
		defer func() { connection.Close(probeContext) }()

		var infrastructure audit.InfrastructureState
		if err := connection.QueryRow(probeContext, auditInfrastructureProbeQuery).Scan(
			&infrastructure.OutboxTableExists, &infrastructure.OperatorLoginExists, &infrastructure.OperatorReadsExist,
		); err != nil {
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
	f.SetAuditReads(audit.NewPoolReadLifecycle(pool))
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
