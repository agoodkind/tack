// Package main prints tests that additional build tags include.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/ci/testselection"
)

func main() {
	if err := testselection.Main(); err != nil {
		slog.Error("testselection.failed", slog.Any("error", err))
		os.Exit(1)
	}
}
