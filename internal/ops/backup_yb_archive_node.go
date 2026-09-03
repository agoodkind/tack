// backup_yb_archive_node.go is the data-guest half of the YugabyteDB snapshot
// export. The orchestrator (yb-snapshot-export) uploads the snapshot metadata,
// schema, and a completeness manifest; this command runs on each data guest,
// tars that guest's own tablet snapshot files out of the local yugabyte
// container, and fills the prefix the manifest assigns to this node's name with
// that tar and the inventory of what it carries.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupYBArchiveNode archives this node's tablet snapshot files for one
// export run. With an explicit runID it archives that run and errors when the
// run's manifest does not list this node; with an empty runID it discovers the
// newest manifest and exits 0 with a log line when there is no manifest, the
// manifest does not list this node, or every artifact this node owes the run
// already exists.
func RunBackupYBArchiveNode(ctx context.Context, cfg *config.Config, runID string) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("yb-archive-node: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return err
	}
	if cfg.BackupYBRocksDBDir == "" {
		err := fmt.Errorf("yb-archive-node: TACK_BACKUP_YB_ROCKSDB_DIR is required")
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return err
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	nodeName, err := localYBNodeName(ctx, cli)
	if err != nil {
		return err
	}

	s3Client := newBackupS3Client(cfg)
	target, err := resolveYBArchiveTarget(ctx, cfg, s3Client, nodeName, runID)
	if err != nil || target == nil {
		return err
	}

	logger.InfoContext(ctx, "backup.yb_archive.start",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("key_prefix", target.prefix),
	)

	stageDir := filepath.Join(cfg.BackupRoot, "yb-archive-"+target.manifest.RunID)
	// The manifest's run id is validated at decode, but this dir is recursively
	// removed on exit, so independently refuse any resolved path that escapes
	// the backup root before creating or deleting anything.
	if !strings.HasPrefix(stageDir, filepath.Clean(cfg.BackupRoot)+string(filepath.Separator)) {
		err := fmt.Errorf("yb-archive-node: stage dir %s escapes backup root %s", stageDir, cfg.BackupRoot)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return err
	}
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		wrapped := fmt.Errorf("mkdir yb archive stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer func() {
		if rmErr := os.RemoveAll(stageDir); rmErr != nil {
			logger.WarnContext(ctx, "backup.yb_archive.stage_cleanup_failed",
				slog.String("dir", stageDir), slog.String("err", rmErr.Error()))
		}
	}()

	tarPath := filepath.Join(stageDir, ybNodeArchiveObject)
	if err := tarYBTabletSnapshots(ctx, cli, cfg, target.manifest.SnapshotID, tarPath); err != nil {
		return err
	}
	// The inventory is recorded from the finished archive and uploaded ahead of
	// it, so no archive is ever in the store without the record of what it
	// carries; every gate and this command's own idempotency probe require
	// both.
	if err := writeYBArchiveInventory(ctx, target.manifest.RunID, nodeName, stageDir, tarPath); err != nil {
		return err
	}
	if err := uploadYBSnapshotArtifacts(ctx, cfg, target.prefix, ybNodeUploadArtifacts(stageDir)); err != nil {
		return err
	}

	logger.InfoContext(ctx, "backup.yb_archive.completed",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key_prefix", target.prefix),
	)
	return nil
}
