// Package servercli implements the operator CLI and the HTTP
// server. With no subcommand it starts the server; subcommands run migrations,
// seeding, audit bundle operations, and the ops maintenance family. Every
// command runs under a root trace span so its output and logs share a trace_id.
package servercli

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

// Run starts the operator CLI or HTTP server and returns its exit code.
//
// Run reads configuration from the environment and command arguments from
// [os.Args]. It closes telemetry before returning to the executable.
//
// The executable handles a failed command with:
//
//	if code := servercli.Run(); code != 0 {
//		os.Exit(code)
//	}
func Run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)

		return 1
	}

	// Mint a per-invocation request id so every command's output and logs share
	// a request_id alongside the span's trace_id and span_id.
	requestID := uuid.NewString()
	factory := cli.System(cfg)
	root := buildRoot(factory)
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
		configureAuditOutbox(factory)
	}

	configureAuditInfrastructureProbe(factory)
	defer factory.CloseAuditOutbox()
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

// exitCodeOf is the process exit status for a failed command: the status the
// error declares through an OperatorExitStatus method, else 1. A command
// declares one when the unit that runs it must tell that failure apart from
// the rest, such as a restore drill that only failed to record its marker and
// must not have its restores repeated. The method name is specific to this
// contract so an error carrying some other process's exit code, such as
// [exec.ExitError], does not set this process's.
func exitCodeOf(err error) int {
	var coded interface{ OperatorExitStatus() int }
	if errors.As(err, &coded) {
		return coded.OperatorExitStatus()
	}

	return 1
}
