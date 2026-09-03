package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// restoreDrillYugabyte restores the newest complete YugabyteDB
// distributed-snapshot export (or the explicitly requested run) into a
// throwaway yugabyted, asserts the auth tables hold rows, and verifies the
// restored audit ledger's hash chain.
func restoreDrillYugabyte(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.yb.start")
	if r.Cfg.BackupYBOverlayPath == "" || r.Cfg.BackupYBRocksDBDir == "" {
		err := fmt.Errorf("restore-drill yb: TACK_BACKUP_YB_OVERLAY_PATH and TACK_BACKUP_YB_ROCKSDB_DIR are required")
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	if err := requireBackupYBImage(ctx, r.Cfg, "restore-drill yb", "backup.restore_drill.yb.failed"); err != nil {
		return err
	}
	s3Client := newBackupS3Client(r.Cfg)

	manifest, err := resolveYBDrillExport(ctx, r, s3Client)
	if err != nil {
		return err
	}

	stageDir := filepath.Join(r.Cfg.BackupRoot, "restore-drill-yb-"+r.RunID)
	if err := os.MkdirAll(stageDir, 0o777); err != nil {
		wrapped := fmt.Errorf("mkdir yb drill stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer os.RemoveAll(stageDir)
	inventories, err := stageYBDrillArtifacts(ctx, r, s3Client, manifest, stageDir)
	if err != nil {
		return err
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

	// Roles first: the schema carries the ledger's grants, and a GRANT naming a
	// role the database does not have fails the schema apply.
	if err := applyYBDrillRoles(ctx, r, name, manifest.Database); err != nil {
		return err
	}
	if err := ybRunSQL(ctx, r, name, manifest.Database, "-c", "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		return err
	}
	if err := ybRunSQL(ctx, r, name, manifest.Database,
		"-v", "ON_ERROR_STOP=1", "-q", "-f", ybDrillArtifactPath(ybSnapshotSchemaObject)); err != nil {
		wrapped := fmt.Errorf("apply schema: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	if err := importAndRestoreYBSnapshot(ctx, r, name, manifest.Database, manifest.SnapshotID, inventories); err != nil {
		return err
	}

	if err := assertYBDrillRows(ctx, r, name, manifest.Database); err != nil {
		return err
	}
	// Rows on their own say nothing about the ledger's hash chain, and a
	// restore is where a chain breaks silently, so the chain is verified from
	// the restored database before this leg passes.
	return assertRestoredLedgerChain(ctx, r, name, manifest.Database)
}

// resolveYBDrillExport picks the export run to drill. With an explicit run key
// (--yb-run-key) that run is used as-is and refused if incomplete, because the
// operator asked for it specifically. Without one the newest complete run
// wins: the window between the orchestrator's upload and the last node's
// archive timer is normal, so manifest-less or incomplete newer runs are
// skipped with a log line naming the reason, and the drill fails only when no
// complete run exists at all.
func resolveYBDrillExport(ctx context.Context, r *restoreDrillCtx, s3Client *s3.Client) (ybSnapshotManifest, error) {
	logger := telemetry.L(ctx)
	if r.YBRunKey != "" {
		return resolveYBDrillExportStrict(ctx, r, s3Client, r.YBRunKey)
	}
	runIDs, err := listYBSnapshotRunIDs(ctx, s3Client, r.Cfg.BackupS3BucketMain)
	if err != nil {
		return ybSnapshotManifest{}, err
	}
	if len(runIDs) == 0 {
		wrapped := fmt.Errorf("no yugabyte-snapshot export in bucket %s", r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.no_export", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	manifest, found, err := newestCompleteYBSnapshotRun(runIDs,
		func(runID string) (ybSnapshotManifest, error) {
			return fetchYBSnapshotManifest(ctx, s3Client, r.Cfg.BackupS3BucketMain, runID)
		},
		func(key string) (bool, error) {
			return objectExists(ctx, s3Client, r.Cfg.BackupS3BucketMain, key)
		},
		func(runID, reason string) {
			logger.InfoContext(ctx, "backup.restore_drill.yb.run_skipped",
				slog.String("run_id", runID), slog.String("reason", reason))
		})
	if err != nil {
		return ybSnapshotManifest{}, err
	}
	if !found {
		wrapped := fmt.Errorf("no complete yugabyte-snapshot export run in bucket %s: every run is missing its manifest or node archives",
			r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.incomplete", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.export", slog.String("run_id", manifest.RunID))
	return manifest, nil
}

// resolveYBDrillExportStrict enforces the completeness gate on one explicitly
// requested export run: the manifest must name the snapshot, the database, and
// at least one tablet-server node, and every listed node prefix must hold its
// archive object. An incomplete run is refused with the missing node names so
// the operator can see which archive timers have not fired yet.
func resolveYBDrillExportStrict(
	ctx context.Context,
	r *restoreDrillCtx,
	s3Client *s3.Client,
	runID string,
) (ybSnapshotManifest, error) {
	logger := telemetry.L(ctx)
	manifest, err := fetchYBSnapshotManifest(ctx, s3Client, r.Cfg.BackupS3BucketMain, runID)
	if err != nil {
		return ybSnapshotManifest{}, err
	}
	if defect := ybDrillManifestDefect(manifest); defect != "" {
		wrapped := fmt.Errorf("yb drill manifest for run %s: %s; refusing the run", runID, defect)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	exists := func(key string) (bool, error) {
		return objectExists(ctx, s3Client, r.Cfg.BackupS3BucketMain, key)
	}
	missingArtifacts, err := missingYBRunArtifacts(manifest, exists)
	if err != nil {
		return ybSnapshotManifest{}, err
	}
	if len(missingArtifacts) > 0 {
		wrapped := fmt.Errorf("yb snapshot export run %s is incomplete: artifacts %s are absent; refusing the run",
			runID, strings.Join(missingArtifacts, ", "))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.incomplete", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	missing, err := missingYBNodeArchives(manifest, exists)
	if err != nil {
		return ybSnapshotManifest{}, err
	}
	if len(missing) > 0 {
		wrapped := fmt.Errorf("yb snapshot export run %s is incomplete: nodes %s have not uploaded their archives; refusing the run",
			runID, strings.Join(missing, ", "))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.incomplete", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.export", slog.String("run_id", runID))
	return manifest, nil
}

// newestCompleteYBSnapshotRun walks runIDs (sorted ascending) newest-first and
// returns the first run whose manifest is present, well-formed, and fully
// archived. skip is called with the run id and the reason for every run passed
// over, so the operator can see why an older export was chosen. found is false
// when no walked run is complete. Fetch errors other than a missing manifest
// and archive-existence errors abort the walk, because a transient store error
// must not silently demote the drill to an older run.
func newestCompleteYBSnapshotRun(
	runIDs []string,
	fetch func(runID string) (ybSnapshotManifest, error),
	exists func(key string) (bool, error),
	skip func(runID, reason string),
) (manifest ybSnapshotManifest, found bool, err error) {
	for _, runID := range slices.Backward(runIDs) {
		candidate, fetchErr := fetch(runID)
		if fetchErr != nil {
			if isObjectNotFound(fetchErr) {
				skip(runID, "manifest not uploaded")
				continue
			}
			return manifest, false, fetchErr
		}
		if defect := ybDrillManifestDefect(candidate); defect != "" {
			skip(runID, defect)
			continue
		}
		missingArtifacts, artifactErr := missingYBRunArtifacts(candidate, exists)
		if artifactErr != nil {
			return manifest, false, artifactErr
		}
		if len(missingArtifacts) > 0 {
			skip(runID, "artifacts "+strings.Join(missingArtifacts, ", ")+" are absent")
			continue
		}
		missing, missingErr := missingYBNodeArchives(candidate, exists)
		if missingErr != nil {
			return manifest, false, missingErr
		}
		if len(missing) > 0 {
			skip(runID, "nodes "+strings.Join(missing, ", ")+" have not uploaded their archives")
			continue
		}
		return candidate, true, nil
	}
	return manifest, false, nil
}

// ybDrillManifestDefect reports why a manifest cannot be drilled, or "" when
// it is well-formed: the manifest must name the snapshot and the database, it
// must declare every artifact the restore opens by name, and it must list at
// least one tablet-server node or it would gate nothing.
//
// The declaration is checked here, against the manifest alone, because it is a
// defect in how the run describes itself rather than a report on the object
// store. A run declaring fewer artifacts than a restore reads (an export
// written before the run carried its own roles file, or one whose declaration
// was truncated) is unusable however many of the objects it named are present:
// the drill would select it as the newest complete run and then fail deep in
// the restore on a path nothing gated. Naming the undeclared artifacts here
// keeps that separate from "the run declared them and one is absent", which
// missingYBRunArtifacts reports and a re-upload fixes.
func ybDrillManifestDefect(manifest ybSnapshotManifest) string {
	if manifest.SnapshotID == "" || manifest.Database == "" {
		return "manifest missing snapshot_id or database"
	}
	if undeclared := undeclaredYBRequiredArtifacts(manifest); len(undeclared) > 0 {
		return "manifest does not declare required artifacts " + strings.Join(undeclared, ", ")
	}
	if len(manifest.Nodes) == 0 {
		return "manifest lists no tablet-server nodes"
	}
	return ""
}

// startScratchYugabyte boots a throwaway yugabyted with the is_port_available
// overlay, advertising on its own container name so the embedded DNS resolves
// it on the IPv6-only bridge. stageDir is bind-mounted read-only at /artifacts.
func startScratchYugabyte(ctx context.Context, r *restoreDrillCtx, name, database, stageDir string) error {
	logger := telemetry.L(ctx)
	if err := ensureImage(ctx, r.Cli, logger, r.Cfg.BackupYBImage); err != nil {
		return err
	}
	cfg := &container.Config{
		Image:    r.Cfg.BackupYBImage,
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
			stageDir + ":" + ybDrillArtifactMount + ":ro",
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

// ysqlshArgs builds a ysqlsh command vector for the scratch's bootstrap user,
// whose name equals the database name (the scratch is created with YSQL_USER
// set to it). Use [ysqlshRoleArgs] to connect as any other role.
func ysqlshArgs(host, database, sql string) []string {
	return ysqlshRoleArgs(host, database, database, sql)
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
