// Package telemetry centralises observability for the tack server. The
// logging stack lives in goodkind.io/gklog. This package is a thin wrapper
// that wires environment-driven configuration, exposes the few helpers
// legacy call sites depend on, and adds tack-specific concerns
// (the Op timer, expvar counters, the Tracer stub).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/gklog"
	"goodkind.io/tack/internal/version"
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

	// OTELEndpoint enables OTLP trace export when non-empty.
	OTELEndpoint string
}

// Setup initializes the global slog logger via gklog. Returns an io.Closer
// the caller must Close on shutdown. Setup is a thin pass-through: it does
// not look at env names, hardcoded paths, or any process state. All policy
// lives in the config file or env vars that populate LogConfig.
func Setup(cfg LogConfig) (io.Closer, error) {
	jsonFile := strings.TrimSpace(cfg.JSONFile)
	textFile := strings.TrimSpace(cfg.TextFile)
	if err := ensureDir(jsonFile); err != nil {
		return nil, err
	}
	if err := ensureDir(textFile); err != nil {
		return nil, err
	}

	rotation := gklog.RotationConfig{
		MaxSizeMB:  cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAgeDays: cfg.MaxAgeDays,
	}
	level := parseLogLevel(cfg.Level)
	handlers := make([]slog.Handler, 0, 3)
	if !cfg.DisableStdout {
		handlers = append(handlers, gklog.StdoutJSON(level))
	}
	if jsonFile != "" {
		handlers = append(handlers, gklog.FileJSON(jsonFile, level, rotation))
	}
	if textFile != "" {
		handlers = append(handlers, gklog.FileText(textFile, "tack", rotation))
	}
	if len(handlers) == 0 {
		return nil, errors.New("logging requires stdout or at least one log file")
	}
	logger, closer := gklog.New(gklog.Config{
		BuildVersion: buildVersion(),
		Handlers:     handlers,
	})
	slog.SetDefault(logger)

	traceCloser, err := setupTracing(cfg.OTELEndpoint)
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, err
	}

	return multiCloser{closers: []io.Closer{traceCloser, closer}}, nil
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

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "debug", "":
		return slog.LevelDebug
	default:
		return slog.LevelDebug
	}
}

func buildVersion() string {
	commit := version.Commit()
	if commit != "dev" && len(commit) > 12 {
		commit = commit[:12]
	}
	if version.Dirty() {
		commit += "+dirty"
	}
	if buildTime := version.BuildTime(); buildTime != "unknown" {
		return fmt.Sprintf("%s built %s", commit, buildTime)
	}
	return commit
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

type multiCloser struct {
	closers []io.Closer
}

func (m multiCloser) Close() error {
	var errs []error
	for i := len(m.closers) - 1; i >= 0; i-- {
		if m.closers[i] == nil {
			continue
		}
		if err := m.closers[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
