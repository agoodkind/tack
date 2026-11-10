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

// writeYBSnapshotManifest validates and serializes the completeness manifest
// so the restore path can find the snapshot the metadata belongs to and verify
// every node archive is present before restoring. Validating on write keeps
// the orchestrator honest against the decode-side allowlist: an export whose
// derived run id or node names would be refused by every reader fails loudly
// here instead of publishing an unusable manifest.
//
// The required-artifact declaration is enforced here too, at the moment the
// export is still running and can still be fixed, rather than only when a
// recovery reaches for the run. It is deliberately not folded into validate():
// that method is the untrusted-input boundary every reader of every run shares,
// including the archive command and the pre-export snapshot cleanup, and runs
// exported before the roles file existed still have to decode there so those
// walks can pass over them by name instead of aborting on the first one.
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
func ybSnapshotUploadArtifacts(stageDir, schemaPath, rolesPath, manifestPath string) []ybSnapshotArtifact {
	return []ybSnapshotArtifact{
		{name: ybSnapshotMetadataObject, path: filepath.Join(stageDir, ybSnapshotMetadataObject)},
		{name: ybSnapshotSchemaObject, path: schemaPath},
		{name: ybSnapshotRolesObject, path: rolesPath},
		{name: ybSnapshotManifestObject, path: manifestPath},
	}
}

// ybSnapshotGatedArtifactNames returns the object names the manifest declares
// as the run's artifacts: every uploaded artifact except the manifest, which
// cannot gate its own presence because its presence is what makes the run
// visible at all. Deriving the declaration from the upload set is what lets a
// new artifact join the completeness gate without the gate naming it.
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

// ybNodeUploadArtifacts returns one node's staged artifacts in upload order,
// each staged under the object name it uploads to so the two can never drift.
// The order is the declaration's, which puts the gated archive last.
func ybNodeUploadArtifacts(stageDir string) []ybSnapshotArtifact {
	objects := ybNodeArtifactObjects()
	files := make([]ybSnapshotArtifact, 0, len(objects))
	for _, object := range objects {
		files = append(files, ybSnapshotArtifact{name: object, path: filepath.Join(stageDir, object)})
	}
	return files
}

// uploadYBSnapshotArtifacts uploads the staged files to the main backup bucket
// under prefix, strictly in slice order and stopping at the first failure.
// Order is load-bearing: the caller places the object others gate on last, so a
// partial upload can never publish a manifest whose gated artifacts are absent,
// nor an archive whose inventory is.
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
