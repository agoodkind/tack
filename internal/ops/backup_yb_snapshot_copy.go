package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// yugabyteBackupContainer is the named live Yugabyte container the compose
// stack ships on each data guest. The per-node archive command runs against
// this container to tar the node's own tablet snapshot files.
const yugabyteBackupContainer = "tack-yugabyte-1"

// tarYBTabletSnapshots streams a gzip tar of every tablet's snapshot directory
// for snapshotID, running find and tar inside the data guest's live yugabyte
// container. Only this node's tablets live under its data dir, so the tar is
// one node's slice of the cluster snapshot. The script exits 3 when no
// snapshot directories match, so an empty tar (a few non-zero bytes, which
// would otherwise pass) cannot report a false success. -print0 with --null
// avoids path-splitting and ARG_MAX limits.
func tarYBTabletSnapshots(ctx context.Context, cli *client.Client, cfg *config.Config, snapshotID, outPath string) error {
	logger := telemetry.L(ctx)
	out, err := os.Create(outPath)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer out.Close()

	script := fmt.Sprintf(
		"set -euo pipefail; cd %q; "+
			"if [ \"$(find . -type d -path '*.snapshots/%s' | wc -l)\" -eq 0 ]; then "+
			"echo 'no tablet snapshot dirs for %s' >&2; exit 3; fi; "+
			"find . -type d -path '*.snapshots/%s' -print0 | tar czf - --null --files-from -",
		cfg.BackupYBRocksDBDir, snapshotID, snapshotID, snapshotID,
	)
	exitCode, stderr, err := containerExecStreaming(ctx, cli, yugabyteBackupContainer,
		[]string{"bash", "-c", script}, nil, out)
	if err != nil {
		wrapped := fmt.Errorf("tablet snapshot tar exec: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("tablet snapshot tar exited %d: %s", exitCode, stderr)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	info, err := os.Stat(outPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if info.Size() == 0 {
		wrapped := fmt.Errorf("tablet snapshot tar produced 0 bytes for snapshot %s", snapshotID)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.tablets_tarred",
		slog.String("snapshot_id", snapshotID), slog.Int64("bytes", info.Size()))
	return nil
}

// writeYBSnapshotManifest serializes the completeness manifest so the restore
// path can find the snapshot the metadata belongs to and verify every node
// archive is present before restoring.
func writeYBSnapshotManifest(ctx context.Context, path string, manifest ybSnapshotManifest) error {
	logger := telemetry.L(ctx)
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		wrapped := fmt.Errorf("marshal yb snapshot manifest: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		wrapped := fmt.Errorf("write yb snapshot manifest %s: %w", path, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// ybSnapshotKeyPrefix is the object-store key prefix for one snapshot-export
// run. The orchestrator's artifacts and every node's archive prefix live
// under it.
func ybSnapshotKeyPrefix(runID string) string {
	return ybSnapshotRootPrefix + runID + "/"
}

// putYBSnapshotObject uploads one staged export artifact. It is a package var
// so tests can swap it to record upload order without an object store.
var putYBSnapshotObject = putObjectFromFile

// ybSnapshotArtifact names one staged export file and the object base name it
// uploads to under the run's key prefix.
type ybSnapshotArtifact struct {
	name string
	path string
}

// ybSnapshotUploadArtifacts returns an export run's staged artifacts in upload
// order, the manifest strictly last. The manifest is the completeness gate the
// archivers and the restore drill trust, so it must land only after every
// object it vouches for.
func ybSnapshotUploadArtifacts(stageDir, schemaPath, manifestPath string) []ybSnapshotArtifact {
	return []ybSnapshotArtifact{
		{name: ybSnapshotMetadataObject, path: filepath.Join(stageDir, ybSnapshotMetadataObject)},
		{name: "schema.sql", path: schemaPath},
		{name: ybSnapshotManifestObject, path: manifestPath},
	}
}

// uploadYBSnapshotArtifacts uploads the staged files to the main backup bucket
// under the run's key prefix, strictly in slice order and stopping at the
// first failure. Order is load-bearing: the caller places the manifest last,
// so a partial upload can never publish a manifest whose gated artifacts are
// absent.
func uploadYBSnapshotArtifacts(ctx context.Context, cfg *config.Config, runID string, files []ybSnapshotArtifact) error {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	prefix := ybSnapshotKeyPrefix(runID)
	for _, file := range files {
		if err := putYBSnapshotObject(ctx, s3Client, cfg.BackupS3BucketMain, prefix+file.name, file.path); err != nil {
			return err
		}
	}
	logger.InfoContext(
		ctx, "backup.yb_snapshot.uploaded",
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("prefix", prefix),
		slog.Int("files", len(files)),
	)
	return nil
}
