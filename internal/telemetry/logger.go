package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"

	"gopkg.in/natefinch/lumberjack.v2"
)

type loggerKey struct{}

// Setup initialises the global slog logger.
//
// If cfg.LogFile is set, logs are written to that file with rotation.
// Logs are always also written to stdout so systemd journal captures them.
// The two writers are fanned out via io.MultiWriter.
func Setup(cfg LogConfig) {
	var writers []io.Writer
	writers = append(writers, os.Stdout)

	if cfg.File != "" {
		writers = append(writers, &lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSizeMB,  // megabytes before rotation
			MaxBackups: cfg.MaxBackups, // rotated files to keep (0 = unlimited)
			MaxAge:     cfg.MaxAgeDays, // days to retain (0 = unlimited)
			Compress:   true,
		})
	}

	w := io.MultiWriter(writers...)

	level := slog.LevelInfo
	if cfg.Level == "debug" {
		level = slog.LevelDebug
	}

	var h slog.Handler
	if cfg.JSON {
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	}

	slog.SetDefault(slog.New(h))
}

// WithLogger stores a logger carrying pre-attached fields into the context.
// Call this in middleware after attaching request_id, workspace_id, etc.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// L returns the logger stored in the context, falling back to the global logger.
// Use this anywhere in the service or adapter layer to get a logger that
// already carries the request's structured fields.
func L(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// LogConfig holds logging configuration.
type LogConfig struct {
	Level      string // "debug" | "info" | "warn" | "error"
	JSON       bool   // true in production, false in dev
	File       string // path to log file; empty = stdout only
	MaxSizeMB  int    // rotate when file exceeds this size (default 100)
	MaxBackups int    // rotated files to retain; 0 = unlimited
	MaxAgeDays int    // days to retain rotated files; 0 = unlimited
}
