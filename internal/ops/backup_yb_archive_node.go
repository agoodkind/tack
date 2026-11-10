// The yb-snapshot-export orchestrator uploads snapshot metadata, schema, and
// a completeness manifest. Each data guest archives tablet snapshot files
// from its local YugabyteDB container and uploads the tar archive and file
// inventory under the prefix that the manifest assigns to its node.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupYBArchiveNode archives this node's tablet snapshot files for an
// export run. An explicit runID selects that run and requires the manifest
// to list this node.
//
// An empty runID selects the newest uploaded manifest. The command logs and
// returns success if a manifest does not exist, the manifest omits this node,
// or the node has already uploaded every required artifact.
//
//   - The tablet archive stores this node's snapshot files.
//   - The inventory records each archived file's path and size.
func RunBackupYBArchiveNode(ctx context.Context, cfg *config.Config, runID string) error {
	if err := requireYBArchiveConfig(ctx, cfg); err != nil {
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

	target, err := resolveYBArchiveTarget(ctx, cfg, newBackupS3Client(cfg), nodeName, runID)
	if err != nil || target == nil || target.complete {
		return err
	}
	return uploadYBNodeArchive(ctx, cfg, cli, nodeName, target)
}

func requireYBArchiveConfig(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("yb-archive-node: object store configuration is incomplete; set TACK_BACKUP_S3_ENDPOINT, TACK_BACKUP_S3_ACCESS_KEY_ID, and TACK_BACKUP_S3_SECRET_ACCESS_KEY")
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return err
	}
	if cfg.BackupYBRocksDBDir == "" {
		err := fmt.Errorf("yb-archive-node: TACK_BACKUP_YB_ROCKSDB_DIR is empty; set it to the RocksDB directory inside the yugabyte container")
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return err
	}
	return nil
}

func uploadYBNodeArchive(ctx context.Context, cfg *config.Config, cli *client.Client, nodeName string, target *ybArchiveTarget) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.yb_archive.start",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("key_prefix", target.prefix),
	)

	stageDir := filepath.Join(cfg.BackupRoot, "yb-archive-"+target.manifest.RunID)
	// Manifest decoding validates the run ID. This function also rejects a
	// staging path outside the backup root before creating files. It removes
	// the staging directory recursively on exit.
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
	// The inventory records the finished archive. The upload writes the
	// inventory before the archive. Completeness checks and this command's
	// repeat-run check require both artifacts, including for archives from
	// before inventories existed.
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
