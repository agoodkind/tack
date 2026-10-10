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
	logger := telemetry.L(ctx)
	cmd := append([]string{"ysqlsh", "-h", ybScratchHost(container), "-p", "5433", "-U", database, "-d", database}, args...)
	env := append([]string{"PGPASSWORD=" + r.YBPass}, extraEnv...)
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container, cmd, env, devNull{})
	if err != nil {
		wrapped := fmt.Errorf("ysqlsh exec: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("ysqlsh exited %d: %s", exitCode, strings.TrimSpace(stderr))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
