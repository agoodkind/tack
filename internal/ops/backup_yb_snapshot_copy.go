package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// yugabyteBackupContainer identifies the live YugabyteDB container that Compose
// runs on each data guest. The node archive command uses that container to
// archive the node's tablet snapshot files.
const yugabyteBackupContainer = "tack-yugabyte-1"

// tarYBTabletSnapshots writes a gzip-compressed tar archive of tablet
// directories for snapshotID. It runs find and tar inside the data guest's live
// YugabyteDB container. That data directory contains only this node's tablets.
//
// The script exits with status 3 when it cannot find matching snapshot
// directories. An empty tar archive still contains nonzero bytes and must not
// count as success. The -print0 and --null options preserve paths without
// splitting and avoid ARG_MAX limits.
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

// writeYBSnapshotManifest validates and serializes a completeness manifest.
// Restore uses it to identify the snapshot and check node archives. Write-time
// validation applies the readers' allowlist to run IDs and node identifiers
// before publishing the manifest.
//
// The writer also requires every artifact that restore opens by name. It
// performs this check during export. The shared validate method remains an
// untrusted-input boundary for readers, archive commands, and cleanup. It
// accepts older manifests exported before the roles artifact existed; those
// operations must be able to decode and skip old runs rather than abort at the
// first one.
func writeYBSnapshotManifest(ctx context.Context, path string, manifest ybSnapshotManifest) error {
	logger := telemetry.L(ctx)
	if err := manifest.validate(); err != nil {
		wrapped := fmt.Errorf("yb snapshot manifest not writable: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if undeclared := undeclaredYBRequiredArtifacts(manifest); len(undeclared) > 0 {
		wrapped := fmt.Errorf("yb snapshot manifest does not declare required artifacts %s; the run would restore into an unusable database",
			strings.Join(undeclared, ", "))
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
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

// ybSnapshotKeyPrefix returns the object-store prefix for one snapshot export
// run. The prefix includes the orchestrator's artifacts and every node archive
// prefix.
func ybSnapshotKeyPrefix(runID string) string {
	return ybSnapshotRootPrefix + runID + "/"
}

// putYBSnapshotObject uploads one staged export artifact. Tests replace this
// package variable to record upload order without an object store.
var putYBSnapshotObject = putObjectFromFile

// ybSnapshotArtifact records a staged file path and the object's base name
// under the run prefix.
type ybSnapshotArtifact struct {
	name string
	path string
}

// ybSnapshotUploadArtifacts returns staged export artifacts in upload order,
// with the manifest last. Archivers and the restore drill use the manifest for
// completeness checks. Every declared object must upload before the manifest.
func ybSnapshotUploadArtifacts(stageDir, schemaPath, rolesPath, manifestPath string) []ybSnapshotArtifact {
	return []ybSnapshotArtifact{
		{name: ybSnapshotMetadataObject, path: filepath.Join(stageDir, ybSnapshotMetadataObject)},
		{name: ybSnapshotSchemaObject, path: schemaPath},
		{name: ybSnapshotRolesObject, path: rolesPath},
		{name: ybSnapshotManifestObject, path: manifestPath},
	}
}

// ybSnapshotGatedArtifactNames returns uploaded object names except the
// manifest. Readers discover the run through manifest presence, which cannot
// independently verify itself. The declaration derives from the upload list and
// includes added artifacts without a separate completeness list.
func ybSnapshotGatedArtifactNames(files []ybSnapshotArtifact) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.name == ybSnapshotManifestObject {
			continue
		}
		names = append(names, file.name)
	}
	return names
}

// ybNodeUploadArtifacts returns a node's staged artifacts in declaration order,
// with the archive last. Each staged filename matches its uploaded object name.
func ybNodeUploadArtifacts(stageDir string) []ybSnapshotArtifact {
	objects := ybNodeArtifactObjects()
	files := make([]ybSnapshotArtifact, 0, len(objects))
	for _, object := range objects {
		files = append(files, ybSnapshotArtifact{name: object, path: filepath.Join(stageDir, object)})
	}
	return files
}

// uploadYBSnapshotArtifacts uploads staged files under prefix in the main
// backup bucket. It preserves slice order and stops at the first failure.
// Callers put the completeness object last: the manifest follows its artifacts,
// and a node archive follows its inventory.
func uploadYBSnapshotArtifacts(ctx context.Context, cfg *config.Config, prefix string, files []ybSnapshotArtifact) error {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
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
