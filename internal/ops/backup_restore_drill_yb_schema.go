package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// Replica mode prevents migration 019's guard from rejecting later audit
// statements in the dump under the scratch bootstrap user.
const ybSchemaApplyOptions = "PGOPTIONS=-c session_replication_role=replica"

func applyYBDrillSchema(ctx context.Context, r *restoreDrillCtx, container, database string) error {
	logger := telemetry.L(ctx)
	// Apply roles before the schema. The schema includes ledger grants, and a GRANT
	// fails if its role does not exist.
	if err := applyYBDrillRoles(ctx, r, container, database); err != nil {
		return err
	}

	if err := ybRunSQL(
		ctx,
		r,
		container,
		database,
		"-c",
		"CREATE EXTENSION IF NOT EXISTS pgcrypto",
	); err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}

	if err := ybRunSQLWithEnv(
		ctx,
		r,
		container,
		database,
		[]string{ybSchemaApplyOptions},
		"-v",
		"ON_ERROR_STOP=1",
		"-q",
		"-f",
		ybDrillArtifactPath(ybSnapshotSchemaObject),
	); err != nil {
		wrapped := fmt.Errorf("apply schema: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// ybRunSQL runs ysqlsh inside the scratch container with the supplied trailing
// arguments and temporary password. A nonzero exit returns an error.
func ybRunSQL(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	database string,
	args ...string,
) error {
	return ybRunSQLWithEnv(ctx, r, container, database, nil, args...)
}

func ybRunSQLWithEnv(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	database string,
	extraEnv []string,
	args ...string,
) error {
	cmd := append(
		[]string{
			"ysqlsh",
			"-h",
			ybScratchHost(container),
			"-p",
			"5433",
			"-U",
			database,
			"-d",
			database,
		},
		args...,
	)
	env := append([]string{"PGPASSWORD=" + r.YBPass}, extraEnv...)
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container, cmd, env, devNull{})
	if err != nil {
		return &ybSQLError{exitCode: 0, stderr: "", cause: err}
	}

	if exitCode != 0 {
		return &ybSQLError{exitCode: exitCode, stderr: strings.TrimSpace(stderr), cause: nil}
	}

	return nil
}

// ybSQLError records a failed ysqlsh run. cause records an exec failure or
// remains nil when ysqlsh runs and exits with a nonzero status.
type ybSQLError struct {
	exitCode int
	stderr   string
	cause    error
}

func (e *ybSQLError) Error() string {
	if e.cause != nil {
		return "ysqlsh exec: " + e.cause.Error()
	}

	return fmt.Sprintf("ysqlsh exited %d: %s", e.exitCode, e.stderr)
}

func (e *ybSQLError) Unwrap() error { return e.cause }
