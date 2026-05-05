package main

import (
	"context"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"

	// Side-effect imports register every operation. Adding a new op only
	// requires creating a file under internal/ops with an init() that calls
	// ops.Register; no edit here.
	_ "goodkind.io/tack/internal/ops"
)

// runOps dispatches the `./server ops <name>` subcommand. With no args it
// prints the registry. With an unknown op name it exits non-zero.
func runOps(cfg *config.Config, args []string) {
	err := ops.RunCommand(context.Background(), cfg, args)
	if err != nil {
		slog.Error("ops.command_failed")
		os.Exit(1)
	}
}
