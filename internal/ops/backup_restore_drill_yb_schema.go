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
	// Roles first: the schema includes the ledger's grants, and a GRANT naming a
	// role the database does not have fails the schema apply.
	if err := applyYBDrillRoles(ctx, r, container, database); err != nil {
		return err
	}
	if err := ybRunSQL(ctx, r, container, database, "-c", "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	if err := ybRunSQLWithEnv(ctx, r, container, database, []string{ybSchemaApplyOptions},
		"-v", "ON_ERROR_STOP=1", "-q", "-f", ybDrillArtifactPath(ybSnapshotSchemaObject)); err != nil {
		wrapped := fmt.Errorf("apply schema: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// ybRunSQL runs ysqlsh with the given trailing args inside the scratch
// container, passing the throwaway password, and errors on a non-zero exit.
func ybRunSQL(ctx context.Context, r *restoreDrillCtx, container, database string, args ...string) error {
	return ybRunSQLWithEnv(ctx, r, container, database, nil, args...)
}

func ybRunSQLWithEnv(
	ctx context.Context,
	r *restoreDrillCtx,
	container, database string,
	extraEnv []string,
	args ...string,
) error {
	cmd := append([]string{"ysqlsh", "-h", ybScratchHost(container), "-p", "5433", "-U", database, "-d", database}, args...)
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

// ybSQLError is one failed ysqlsh run. cause is the exec failure, or nil when
// ysqlsh ran and exited non-zero.
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
