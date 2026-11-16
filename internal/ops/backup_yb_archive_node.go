// Data guests archive their local tablet snapshots and upload each tar and
// inventory to the node prefix assigned by the export manifest. The export
// orchestrator supplies metadata, schema, and that completeness manifest.

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

// RunBackupYBArchiveNode archives this node for an explicit run or discovers
// the newest manifest when runID is empty. Explicit runs require membership.
// Discovery succeeds without upload when manifests are absent, the node is
// unlisted, or all required artifacts already exist, and logs the reason.
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
		err := fmt.Errorf(
			"yb-archive-node: object store configuration is incomplete; set TACK_BACKUP_S3_ENDPOINT, TACK_BACKUP_S3_ACCESS_KEY_ID, and TACK_BACKUP_S3_SECRET_ACCESS_KEY",
		)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))

		return err
	}

	if cfg.BackupYBRocksDBDir == "" {
		err := fmt.Errorf(
			"yb-archive-node: TACK_BACKUP_YB_ROCKSDB_DIR is empty; set it to the RocksDB directory inside the yugabyte container",
		)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))

		return err
	}

	return nil
}

func uploadYBNodeArchive(
	ctx context.Context,
	cfg *config.Config,
	cli *client.Client,
	nodeName string,
	target *ybArchiveTarget,
) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(
		ctx,
		"backup.yb_archive.start",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("key_prefix", target.prefix),
	)

	stageDir := filepath.Join(cfg.BackupRoot, "yb-archive-"+target.manifest.RunID)
	// Decode validates the run ID. This independent containment check protects
	// recursive staging cleanup before creating or deleting any directory.
	if !strings.HasPrefix(stageDir, filepath.Clean(cfg.BackupRoot)+string(filepath.Separator)) {
		err := fmt.Errorf(
			"yb-archive-node: stage dir %s escapes backup root %s",
			stageDir,
			cfg.BackupRoot,
		)
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
			logger.WarnContext(
				ctx,
				"backup.yb_archive.stage_cleanup_failed",
				slog.String("dir", stageDir),
				slog.String("err", rmErr.Error()),
			)
		}
	}()

	tarPath := filepath.Join(stageDir, ybNodeArchiveObject)
	if err := tarYBTabletSnapshots(ctx, cli, cfg, target.manifest.SnapshotID, tarPath); err != nil {
		return err
	}

	// Inventory records the finished archive and uploads before it.
	// Completeness and idempotency checks require both objects.
	if err := writeYBArchiveInventory(
		ctx,
		target.manifest.RunID,
		nodeName,
		stageDir,
		tarPath,
	); err != nil {
		return err
	}

	if err := uploadYBSnapshotArtifacts(
		ctx,
		cfg,
		target.prefix,
		ybNodeUploadArtifacts(stageDir),
	); err != nil {
		return err
	}

	logger.InfoContext(
		ctx,
		"backup.yb_archive.completed",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key_prefix", target.prefix),
	)

	return nil
}
