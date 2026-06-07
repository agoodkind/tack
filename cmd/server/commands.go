package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/version"
	"goodkind.io/tack/migrations"
)

// emptyInput is the input for top-level commands that take no flags or args.
type emptyInput struct {
	clispec.InputMarker
}

// buildRoot assembles the cobra command tree. The bare command starts the
// server; serve, migrate, seed, the audit bundle family, and the ops
// maintenance family are subcommands rendered from one declarative registry.
func buildRoot(f *cli.Factory) *cobra.Command {
	root := &cobra.Command{
		Use:           "tack",
		Short:         "Tack operator CLI and server",
		Version:       version.Tag(),
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runServer(f.Cfg)
			return nil
		},
	}
	root.SetIn(f.In)
	root.SetOut(f.Out)
	root.SetErr(f.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	f.RegisterGlobalFlags(root)

	reg := clispec.NewRegistry()
	clispec.Register(reg, serveOp(f))
	clispec.Register(reg, migrateOp(f))
	clispec.Register(reg, seedOp(f))
	registerAudit(reg, f)
	ops.RegisterCommands(reg, f)
	for _, command := range clispec.RenderCobra(reg, f) {
		root.AddCommand(command)
	}
	return root
}

func serveOp(f *cli.Factory) clispec.Operation[emptyInput] {
	return clispec.Operation[emptyInput]{
		Name:  clispec.Name{Canonical: "serve"},
		Short: "Start the HTTP server (the default action with no subcommand)",
		New:   func() emptyInput { return emptyInput{} },
		Run: func(_ context.Context, _ emptyInput, _ clispec.ResultSink) error {
			runServer(f.Cfg)
			return nil
		},
	}
}

func migrateOp(f *cli.Factory) clispec.Operation[emptyInput] {
	return clispec.Operation[emptyInput]{
		Name:  clispec.Name{Canonical: "migrate"},
		Short: "Run database migrations against DATABASE_URL",
		New:   func() emptyInput { return emptyInput{} },
		Run: func(ctx context.Context, _ emptyInput, sink clispec.ResultSink) error {
			if err := postgres.Migrate(ctx, f.Cfg.DatabaseURL, migrations.FS); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			return sink.Text(ctx, "migrations complete")
		},
	}
}
