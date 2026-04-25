// Package telemetry centralises observability for the tack server. The
// logging stack lives in goodkind.io/gklog. This package is a thin wrapper
// that wires environment-driven configuration, exposes the few helpers
// legacy call sites depend on, and adds tack-specific concerns
// (the Op timer, expvar counters, the Tracer stub).
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/gklog"
)

// LogConfig is what Setup needs to wire gklog. Every field is taken
// verbatim from the caller. Setup never branches on environment names. The
// dev-vs-prod difference lives entirely in the env vars consumed by
// internal/config.
type LogConfig struct {
	// Level is the minimum log level. Empty defers to gklog's default
	// (debug). Accepted values: "debug", "info", "warn", "error".
	Level string

	// JSONFile is the JSON-line log path. Empty disables JSON-file output.
	JSONFile string
	// TextFile is the human-readable text log path. Empty disables it.
	TextFile string
	// DisableStdout suppresses the stdout JSON handler. Production usually
	// wants stdout enabled so journald scrapes it; CLI-style processes
	// where stdout is part of a contract may want it off.
	DisableStdout bool

	// MaxSizeMB caps each rotated log file. Zero defers to gklog's default.
	MaxSizeMB int
	// MaxBackups limits the number of rotated files to keep. Zero means
	// keep them all forever (gklog passes 0 through to lumberjack as
	// unlimited). Set to 0 in dev configs to retain history.
	MaxBackups int
	// MaxAgeDays caps rotated-file age. Zero means keep forever.
	MaxAgeDays int
}

// Setup initializes the global slog logger via gklog. Returns an io.Closer
// the caller must Close on shutdown. Setup is a thin pass-through: it does
// not look at env names, hardcoded paths, or any process state. All policy
// lives in the config file or env vars that populate LogConfig.
func Setup(cfg LogConfig) (io.Closer, error) {
	gcfg := gklog.Config{
		JSONLogFile:   strings.TrimSpace(cfg.JSONFile),
		TextLogFile:   strings.TrimSpace(cfg.TextFile),
		TextLabel:     "tack",
		JSONMinLevel:  cfg.Level,
		DisableStdout: cfg.DisableStdout,
		Rotation: gklog.RotationConfig{
			MaxSizeMB:  cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAgeDays: cfg.MaxAgeDays,
		},
	}

	if err := ensureDir(gcfg.JSONLogFile); err != nil {
		return nil, err
	}
	if err := ensureDir(gcfg.TextLogFile); err != nil {
		return nil, err
	}

	logger, closer, err := gklog.New(gcfg)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return closer, nil
}

func ensureDir(path string) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// WithLogger stores a logger carrying pre-attached fields into the context.
// Mirrors gklog.WithLogger so existing callers in this codebase keep
// working unchanged.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return gklog.WithLogger(ctx, l)
}

// L returns the logger stored in ctx, falling back to slog.Default. Wraps
// gklog.L so call sites in tack do not need to import gklog directly.
func L(ctx context.Context) *slog.Logger {
	return gklog.L(ctx)
}
