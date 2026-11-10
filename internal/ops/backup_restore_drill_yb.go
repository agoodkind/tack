package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// restoreDrillYugabyte restores the newest complete distributed-snapshot
// export, or an explicitly requested run, into a temporary yugabyted. It checks
// that the auth tables contain rows and verifies the restored audit chains.
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

	startWatch := newYBScratchWatch(r, name, "", []string{"PGPASSWORD=" + r.YBPass},
		ysqlshArgs(ybScratchHost(name), manifest.Database, "select 1"))
	took, err := awaitYBScratch(ctx, "scratch yugabyted start", startWatch,
		ybScratchStallWindow, ybScratchPollInterval, ybScratchProbeTimeout)
	if err != nil {
		wrapped := fmt.Errorf("scratch yugabyted never became ready: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.scratch_ready", slog.Duration("took", took))

	if err := applyYBDrillSchema(ctx, r, name, manifest.Database); err != nil {
		return err
	}

	if err := importAndRestoreYBSnapshot(ctx, r, name, manifest.Database, manifest.SnapshotID, inventories); err != nil {
		return err
	}

	if err := assertYBDrillRows(ctx, r, name, manifest.Database); err != nil {
		return err
	}
	// Row counts do not establish hash-chain integrity. The drill verifies the
	// chain from the restored database before this check can pass.
	return assertRestoredLedgerChain(ctx, r, name, manifest.Database)
}

// resolveYBDrillExport selects the export run. An explicit --yb-run-key must
// identify a complete run; the drill refuses an incomplete requested run.
// Without that option, it selects the newest complete run. Uploads can remain
// incomplete between the orchestrator upload and the final node archive timer.
// The drill logs why it skips newer runs with missing manifests or archives.
// It fails when it cannot select a complete run.
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

// resolveYBDrillExportStrict checks one explicitly requested export. Its
// manifest must specify a snapshot, database, and at least one tablet-server
// node. Every listed node prefix must contain an archive. The refusal reports
// missing nodes and identifies their unfinished archive uploads.
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

// newestCompleteYBSnapshotRun reads ascending runIDs in reverse order and
// selects the first run with a present, valid manifest and complete archives.
// It invokes skip with each rejected run ID and reason. found remains false
// when every run is incomplete.
//
// A fetch error other than an absent manifest stops selection. An object
// existence-check error also stops it. A transient store error must not select
// an older run silently.
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

// ybDrillManifestDefect returns a reason that the manifest cannot support a
// restore, or an empty string when it declares a snapshot, database, every
// required artifact, and at least one tablet-server node.
//
// This check examines declarations rather than object-store contents. An old
// export that predates the roles-file declaration, or a truncated declaration,
// cannot qualify even when every declared object exists. Otherwise selection
// could accept a run and fail later on an artifact it never checked.
// missingYBRunArtifacts separately checks declared objects; re-uploading an
// absent object can repair that failure without correcting the manifest.
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

// startScratchYugabyte starts a temporary yugabyted with the is_port_available
// overlay. It advertises [ybScratchHost] through both the container hostname
// and network alias on the IPv6-only bridge. The engine uses a fresh per-run
// base directory under the backup root. stageDir mounts read-only at
// /artifacts.
func startScratchYugabyte(ctx context.Context, r *restoreDrillCtx, name, database, stageDir string) error {
	logger := telemetry.L(ctx)
	if err := ensureImage(ctx, r.Cli, logger, r.Cfg.BackupYBImage); err != nil {
		return err
	}
	baseDir, err := makeDrillScratchDir(ctx, r, r.Cfg.BackupYBImage, "yb")
	if err != nil {
		return err
	}
	host := ybScratchHost(name)
	cfg := &container.Config{
		Image:    r.Cfg.BackupYBImage,
		Hostname: host,
		Env: []string{
			"YSQL_DB=" + database,
			"YSQL_USER=" + database,
			"YSQL_PASSWORD=" + r.YBPass,
		},
		Entrypoint: []string{"/home/yugabyte/bin/yugabyted"},
		Cmd: []string{
			"start", "--daemon=false",
			"--base_dir=/home/yugabyte/var",
			"--advertise_address=" + host,
			"--listen=" + host,
		},
	}
	hostCfg := &container.HostConfig{
		Binds: []string{
			r.Cfg.BackupYBOverlayPath + ":/home/yugabyte/bin/yugabyted:ro",
			stageDir + ":" + ybDrillArtifactMount + ":ro",
			baseDir + ":/home/yugabyte/var",
		},
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: ybScratchNetworking(r.Cfg.BackupFDBNetwork, host),
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

// ysqlshArgs builds command arguments for the scratch bootstrap user. The
// scratch configuration sets YSQL_USER to the database name. Use
// [ysqlshRoleArgs] for another role.
func ysqlshArgs(host, database, sql string) []string {
	return ysqlshRoleArgs(host, database, database, sql)
}
