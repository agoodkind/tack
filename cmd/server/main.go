// Command server builds the `tack` binary: the operator CLI and the HTTP
// server. With no subcommand it starts the server; subcommands run migrations,
// seeding, audit bundle operations, and the ops maintenance family. Every
// command runs under a root trace span so its output and logs share a trace_id.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
)

func main() {
	if code := run(); code != 0 {
		slog.Error("cli.exited", slog.Int("code", code))
		os.Exit(code)
	}
}

// run owns startup and dispatch and returns a process exit code, so [os.Exit]
// stays in main and the telemetry closer's deferred Close always runs.
func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		return 1
	}

	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      cfg.LogJSONFile,
		TextFile:      cfg.LogTextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
		OTELEndpoint:  cfg.OTELEndpoint,
	})
	if err != nil {
		slog.Error("telemetry.setup", "err", err)
		return 1
	}
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()
	slog.Info("server.startup",
		slog.String("env", cfg.Env),
		slog.String("version", version.Tag()),
		slog.String("commit", version.Commit()),
	)

	f := cli.System(cfg)
	ctx, span := telemetry.StartSpan(context.Background(), "cli")
	defer span.End()
	// Mint a per-invocation request id so every command's output and logs share
	// a request_id alongside the span's trace_id and span_id.
	ctx = telemetry.WithRequestMetadata(ctx, uuid.NewString())
	ctx = telemetry.WithTraceLogger(ctx)
	// Log one correlated line per invocation so a command's output envelope
	// trace_id can be matched against the run's log trail.
	telemetry.L(ctx).InfoContext(ctx, "cli.start")

	root := buildRoot(f)
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "cli.failed", slog.String("err", err.Error()))
		return 1
	}
	return 0
}
