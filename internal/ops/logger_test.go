package ops

import (
	"io"
	"log/slog"
)

// nopLogger is the logger tests hand to code that logs progress, so a test's
// output holds only its own findings.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
