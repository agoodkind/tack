// backup_yb_archive_node.go is the data-guest half of the YugabyteDB snapshot
// export. The orchestrator (yb-snapshot-export) uploads the snapshot metadata,
// schema, and a completeness manifest; this command runs on each data guest,
// tars that guest's own tablet snapshot files out of the local yugabyte
// container, and uploads the tar under the prefix the manifest assigns to this
// node's name. The node timers fire independently after the orchestrator's, so
// finding nothing to do is a quiet success, not an error.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybArchiveTarget is the resolved work for one archive invocation: the run's
// manifest and the object key this node must fill.
type ybArchiveTarget struct {
	manifest ybSnapshotManifest
	key      string
}

// RunBackupYBArchiveNode archives this node's tablet snapshot files for one
// export run. With an explicit runID it archives that run and errors when the
// run's manifest does not list this node; with an empty runID it discovers the
// newest manifest and exits 0 with a log line when there is no manifest, the
// manifest does not list this node, or this node's archive already exists.
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
		slog.String("key", target.key),
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
	if err := putObjectFromFile(ctx, s3Client, cfg.BackupS3BucketMain, target.key, tarPath); err != nil {
		return err
	}

	logger.InfoContext(ctx, "backup.yb_archive.completed",
		slog.String("run_id", target.manifest.RunID),
		slog.String("snapshot_id", target.manifest.SnapshotID),
		slog.String("node", nodeName),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key", target.key),
	)
	return nil
}

// resolveYBArchiveTarget decides what, if anything, this node must archive.
// A nil target with a nil error is the quiet nothing-to-do outcome, reachable
// only in discovery mode (empty runID): no run has a manifest yet, the newest
// manifest does not list this node, or this node's archive is already
// uploaded. The orchestrator uploads the manifest last, so discovery walks
// past manifest-less run prefixes (runs that never finished) instead of
// treating them as errors. With an explicit runID a missing manifest or an
// unlisted node is an error, because the operator asked for that run
// specifically.
func resolveYBArchiveTarget(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	nodeName, runID string,
) (*ybArchiveTarget, error) {
	logger := telemetry.L(ctx)
	discovered := runID == ""
	var manifest ybSnapshotManifest
	if discovered {
		newest, found, err := newestUploadedYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain)
		if err != nil {
			return nil, err
		}
		if !found {
			logger.InfoContext(ctx, "backup.yb_archive.no_manifest", slog.String("node", nodeName))
			return nil, nil
		}
		manifest = newest
	} else {
		fetched, err := fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
		if err != nil {
			return nil, err
		}
		manifest = fetched
	}

	prefix, listed := manifest.nodePrefix(nodeName)
	if !listed {
		if discovered {
			logger.InfoContext(ctx, "backup.yb_archive.node_not_listed",
				slog.String("run_id", manifest.RunID), slog.String("node", nodeName))
			return nil, nil
		}
		err := fmt.Errorf("yb-archive-node: manifest for run %s does not list node %q", runID, nodeName)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return nil, err
	}

	key := ybSnapshotKeyPrefix(manifest.RunID) + prefix + ybNodeArchiveObject
	present, err := objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
	if err != nil {
		return nil, err
	}
	if present {
		logger.InfoContext(ctx, "backup.yb_archive.already_archived",
			slog.String("run_id", manifest.RunID), slog.String("node", nodeName), slog.String("key", key))
		return nil, nil
	}
	return &ybArchiveTarget{manifest: manifest, key: key}, nil
}

// localYBNodeName is the node name this guest's yugabyte container announces
// to the cluster. The compose deploy sets the container's hostname to the
// node's permanent name (never an address), the same name the masters report
// in list_all_tablet_servers and the manifest lists, so the container hostname
// is the node's identity.
func localYBNodeName(ctx context.Context, cli *client.Client) (string, error) {
	logger := telemetry.L(ctx)
	insp, err := cli.ContainerInspect(ctx, yugabyteBackupContainer, client.ContainerInspectOptions{})
	if err != nil {
		wrapped := fmt.Errorf("inspect local yugabyte container %q: %w", yugabyteBackupContainer, err)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if insp.Container.Config == nil || strings.TrimSpace(insp.Container.Config.Hostname) == "" {
		wrapped := fmt.Errorf("local yugabyte container %q has no hostname to identify this node", yugabyteBackupContainer)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return insp.Container.Config.Hostname, nil
}
