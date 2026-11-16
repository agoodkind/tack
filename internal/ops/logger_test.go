package ops

import (
	"io"
	"log/slog"
)

// nopLogger discards progress logs during tests.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
