package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// ybRolePattern matches the audit role names the exported schema's row-level
// security policies reference; the restore must create them before applying the
// schema or the policy statements fail.
var ybRolePattern = regexp.MustCompile(`\b(?:tack_)?audit_[a-z_]+\b`)

// restoreDrillYugabyte restores the latest YugabyteDB distributed-snapshot
// export into a throwaway yugabyted and asserts the auth tables hold rows.
func restoreDrillYugabyte(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.yb.start")
	if r.Cfg.BackupYBOverlayPath == "" || r.Cfg.BackupYBRocksDBDir == "" {
		err := fmt.Errorf("restore-drill yb: TACK_BACKUP_YB_OVERLAY_PATH and TACK_BACKUP_YB_ROCKSDB_DIR are required")
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	s3Client := newBackupS3Client(r.Cfg)

	prefixes, err := listImmediatePrefixes(ctx, s3Client, r.Cfg.BackupS3BucketMain, "yugabyte-snapshot/")
	if err != nil {
		return err
	}
	if len(prefixes) == 0 {
		wrapped := fmt.Errorf("no yugabyte-snapshot export in bucket %s", r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.no_export", slog.String("err", wrapped.Error()))
		return wrapped
	}
	sort.Strings(prefixes)
	srcPrefix := prefixes[len(prefixes)-1]
	logger.InfoContext(ctx, "backup.restore_drill.yb.export", slog.String("prefix", srcPrefix))

	stageDir := filepath.Join(r.Cfg.BackupRoot, "restore-drill-yb-"+r.RunID)
	if err := os.MkdirAll(stageDir, 0o777); err != nil {
		wrapped := fmt.Errorf("mkdir yb drill stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer os.RemoveAll(stageDir)
	for _, name := range []string{"manifest.json", "metadata.snapshot", "schema.sql", "tablets.tar.gz"} {
		if err := getObjectToFile(ctx, s3Client, r.Cfg.BackupS3BucketMain, srcPrefix+name, filepath.Join(stageDir, name)); err != nil {
			return err
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(stageDir, "manifest.json"))
	if err != nil {
		wrapped := fmt.Errorf("read yb drill manifest: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	var manifest struct {
		SnapshotID string `json:"snapshot_id"`
		Database   string `json:"database"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		wrapped := fmt.Errorf("unmarshal yb drill manifest: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if manifest.SnapshotID == "" || manifest.Database == "" {
		wrapped := fmt.Errorf("yb drill manifest missing snapshot_id or database")
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	name := "tack-rtyb-" + r.RunID
	if err := startScratchYugabyte(ctx, r, name, manifest.Database, stageDir); err != nil {
		return err
	}

	if err := waitExecOK(ctx, r, name, 120*time.Second,
		[]string{"PGPASSWORD=" + r.YBPass},
		ysqlshArgs(name, manifest.Database, "select 1")); err != nil {
		wrapped := fmt.Errorf("scratch yugabyted never became ready: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	if err := createYBDrillRoles(ctx, r, name, manifest.Database, filepath.Join(stageDir, "schema.sql")); err != nil {
		return err
	}
	if err := ybRunSQL(ctx, r, name, manifest.Database, "-c", "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		return err
	}
	if err := ybRunSQL(ctx, r, name, manifest.Database, "-v", "ON_ERROR_STOP=1", "-q", "-f", "/artifacts/schema.sql"); err != nil {
		wrapped := fmt.Errorf("apply schema: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	if err := importAndRestoreYBSnapshot(ctx, r, name, manifest.Database, manifest.SnapshotID); err != nil {
		return err
	}

	return assertYBDrillRows(ctx, r, name, manifest.Database)
}

// startScratchYugabyte boots a throwaway yugabyted with the is_port_available
// overlay, advertising on its own container name so the embedded DNS resolves
// it on the IPv6-only bridge. stageDir is bind-mounted read-only at /artifacts.
func startScratchYugabyte(ctx context.Context, r *restoreDrillCtx, name, database, stageDir string) error {
	logger := telemetry.L(ctx)
	if err := ensureImage(ctx, r.Cli, logger, r.Cfg.BackupYBPITRImage); err != nil {
		return err
	}
	cfg := &container.Config{
		Image:    r.Cfg.BackupYBPITRImage,
		Hostname: name,
		Env: []string{
			"YSQL_DB=" + database,
			"YSQL_USER=" + database,
			"YSQL_PASSWORD=" + r.YBPass,
		},
		Entrypoint: []string{"/home/yugabyte/bin/yugabyted"},
		Cmd: []string{
			"start", "--daemon=false",
			"--base_dir=/home/yugabyte/var",
			"--advertise_address=" + name,
			"--listen=" + name,
		},
	}
	hostCfg := &container.HostConfig{
		Binds: []string{
			r.Cfg.BackupYBOverlayPath + ":/home/yugabyte/bin/yugabyted:ro",
			stageDir + ":/artifacts:ro",
		},
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: netMode(r.Cfg.BackupFDBNetwork),
		Name:             name,
	})
	if err != nil {
		wrapped := fmt.Errorf("create scratch yugabyted %s: %w", name, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	r.trackContainer(name)
	if _, err := r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		wrapped := fmt.Errorf("start scratch yugabyted %s: %w", name, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// ysqlshArgs builds a ysqlsh command vector. The user equals the database name
// (the scratch is created with YSQL_USER set to it).
func ysqlshArgs(host, database, sql string) []string {
	return []string{"ysqlsh", "-h", host, "-p", "5433", "-U", database, "-d", database, "-tAc", sql}
}

// ybRunSQL runs ysqlsh with the given trailing args inside the scratch
// container, passing the throwaway password, and errors on a non-zero exit.
func ybRunSQL(ctx context.Context, r *restoreDrillCtx, container, database string, args ...string) error {
	logger := telemetry.L(ctx)
	cmd := append([]string{"ysqlsh", "-h", container, "-p", "5433", "-U", database, "-d", database}, args...)
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container, cmd,
		[]string{"PGPASSWORD=" + r.YBPass}, devNull{})
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

// createYBDrillRoles creates every audit role the schema references, as NOLOGIN
// roles, so the schema's row-level-security policies apply. A duplicate-role
// error is ignored.
func createYBDrillRoles(ctx context.Context, r *restoreDrillCtx, container, database, schemaPath string) error {
	logger := telemetry.L(ctx)
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		wrapped := fmt.Errorf("read schema for roles: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	seen := map[string]bool{}
	for _, role := range ybRolePattern.FindAllString(string(schema), -1) {
		if seen[role] {
			continue
		}
		seen[role] = true
		// Best effort: a role that already exists is fine.
		_, _, _ = containerExecStreaming(ctx, r.Cli, container,
			[]string{"ysqlsh", "-h", container, "-p", "5433", "-U", database, "-d", database, "-c", "CREATE ROLE \"" + role + "\""},
			[]string{"PGPASSWORD=" + r.YBPass}, devNull{})
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.roles_created", slog.Int("count", len(seen)))
	return nil
}

// assertYBDrillRows fails unless the restored auth tables hold rows.
func assertYBDrillRows(ctx context.Context, r *restoreDrillCtx, container, database string) error {
	logger := telemetry.L(ctx)
	rowCounts := make([]any, 0, 3)
	for _, table := range []string{"users", "api_tokens", "org_members"} {
		var buf bytes.Buffer
		exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container,
			ysqlshArgs(container, database, "select count(*) from "+table),
			[]string{"PGPASSWORD=" + r.YBPass}, &buf)
		if err != nil || exitCode != 0 {
			wrapped := fmt.Errorf("count %s: exit %d: %s: %w", table, exitCode, strings.TrimSpace(stderr), err)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		count := strings.TrimSpace(buf.String())
		if count == "0" {
			wrapped := fmt.Errorf("restored table %s has 0 rows", table)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		rowCounts = append(rowCounts, slog.String(table, count))
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.rows", rowCounts...)
	return nil
}
