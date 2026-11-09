// Package main runs the ci-image command.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/ci/image"
)

func main() {
	err := image.NewCommand().Execute()
	if err != nil {
		slog.Error("ci-image.failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
