// Command server builds the tack operator CLI and HTTP server. The bare
// command starts the server. Subcommands run migrations, seeding, audit
// bundle operations, and ops maintenance. Command execution uses a root
// trace span to correlate output and logs by trace_id.
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

// run starts the application, dispatches the command, and returns a process
// exit code. Its deferred telemetry close runs before main calls os.Exit.
func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		return 1
	}

	// A new request ID identifies this invocation in output and logs alongside
	// the span's trace_id and span_id.
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
	slog.Info("server.startup",
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
	// The start log and command output use the same trace_id to identify this
	// invocation.
	telemetry.L(ctx).InfoContext(ctx, "cli.start")
	if logs.CommandDir != "" {
		if removeExpiredCommandLogs(ctx, logs.CommandDir, cfg.LogMaxAgeDays) != nil {
			return 1
		}
	}

	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		code := exitCodeOf(err)
		telemetry.L(ctx).ErrorContext(ctx, "cli.failed",
			slog.String("err", err.Error()), slog.Int("code", code))
		return code
	}
	return 0
}

// exitCodeOf returns the error's OperatorExitStatus value or 1 when the
// error does not implement that method. A restore drill uses a distinct
// status for a marker write failure after successful restores, which lets
// the caller avoid repeating them. The specific method prevents a child
// process error such as exec.ExitError from setting this process's status
// through its ExitCode method.
func exitCodeOf(err error) int {
	var coded interface{ OperatorExitStatus() int }
	if errors.As(err, &coded) {
		return coded.OperatorExitStatus()
	}
	return 1
}
