package main

import (
	"context"
	"fmt"
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
	if len(args) == 0 || args[0] == "list" {
		printOpList()
		return
	}
	name := args[0]
	if _, ok := ops.Get(name); !ok {
		fmt.Fprintf(os.Stderr, "unknown op: %s\n\n", name)
		printOpList()
		os.Exit(1)
	}
	ctx := context.Background()
	if err := ops.Run(ctx, cfg, name); err != nil {
		slog.Error("ops.run", "op", name, "err", err)
		os.Exit(1)
	}
}

func printOpList() {
	fmt.Fprintln(os.Stderr, "available ops:")
	for _, op := range ops.List() {
		fmt.Fprintf(os.Stderr, "  %-32s %s\n", op.Name, op.Description)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "usage: ./server ops <name>")
}
