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
		slog.Error("ci-image.failed", slog.Any("error", err))
		os.Exit(1)
	}
}
