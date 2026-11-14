// Command server builds the `tack` binary: the operator CLI and the HTTP
// server. With no subcommand it starts the server; subcommands run migrations,
// seeding, audit bundle operations, and the ops maintenance family. Every
// command runs under a root trace span so its output and logs share a trace_id.
package main

import (
	"context"
	"errors"
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

	// Mint a per-invocation request id so every command's output and logs share
	// a request_id alongside the span's trace_id and span_id.
	requestID := uuid.NewString()
	f := cli.System(cfg)
	root := buildRoot(f)
	logs := processLogFiles(cfg, root, os.Args[1:], requestID)
	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      logs.JSONFile,
		TextFile:      logs.TextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
		OTELEndpoint:  cfg.OTELEndpoint,
		Env:           cfg.Env,
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
	slog.Info(
		"server.startup",
		slog.String("version", version.Tag()),
		slog.String("commit", version.Commit()),
	)

	target, _, findErr := root.Find(os.Args[1:])
	if findErr != nil || target.CommandPath() != "tack ops audit seed-roles" {
		configureAuditOutbox(f)
	}

	configureAuditInfrastructureProbe(f)
	defer f.CloseAuditOutbox()
	ctx, span := telemetry.StartSpan(context.Background(), "cli")
	defer span.End()
	ctx = telemetry.WithRequestMetadata(ctx, requestID)
	ctx = telemetry.WithTraceLogger(ctx)
	// Log one correlated line per invocation so a command's output envelope
	// trace_id can be matched against the run's log trail.
	telemetry.L(ctx).InfoContext(ctx, "cli.start")
	if logs.CommandDir != "" {
		if removeExpiredCommandLogs(ctx, logs.CommandDir, cfg.LogMaxAgeDays) != nil {
			return 1
		}
	}

	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		code := exitCodeOf(err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"cli.failed",
			slog.String("err", err.Error()),
			slog.Int("code", code),
		)

		return code
	}

	return 0
}

// exitCodeOf returns a command error's OperatorExitStatus, or 1 by default.
// The method distinguishes failures such as a restore marker write error that
// must not trigger another restore. Other process exit statuses, including
// exec.ExitError values, do not determine this process's status.
func exitCodeOf(err error) int {
	var coded interface{ OperatorExitStatus() int }
	if errors.As(err, &coded) {
		return coded.OperatorExitStatus()
	}

	return 1
}
