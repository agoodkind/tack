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

// yugabyteBackupContainer identifies the live data-guest container used to
// archive that node's local tablet snapshots.
const yugabyteBackupContainer = "tack-yugabyte-1"

// tarYBTabletSnapshots archives this node's matching snapshot directories using
// find and tar inside the live container. Exit 3 rejects absent directories
// instead of accepting a nonempty gzip header as a valid archive. Null-delimited
// paths prevent word splitting and command-line size limits.
func tarYBTabletSnapshots(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	snapshotID string,
	outPath string,
) error {
	logger := telemetry.L(ctx)
	out, err := os.Create(outPath)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", outPath, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.tablets_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}
	defer out.Close()

	script := fmt.Sprintf(
		"set -euo pipefail; cd %q; "+
			"if [ \"$(find . -type d -path '*.snapshots/%s' | wc -l)\" -eq 0 ]; then "+
			"echo 'no tablet snapshot dirs for %s' >&2; exit 3; fi; "+
			"find . -type d -path '*.snapshots/%s' -print0 | tar czf - --null --files-from -",
		cfg.BackupYBRocksDBDir,
		snapshotID,
		snapshotID,
		snapshotID,
	)
	exitCode, stderr, err := containerExecStreaming(
		ctx,
		cli,
		yugabyteBackupContainer,
		[]string{"bash", "-c", script},
		nil,
		out,
	)
	if err != nil {
		wrapped := fmt.Errorf("tablet snapshot tar exec: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.tablets_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if exitCode != 0 {
		wrapped := fmt.Errorf("tablet snapshot tar exited %d: %s", exitCode, stderr)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.tablets_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	info, err := os.Stat(outPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", outPath, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.tablets_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if info.Size() == 0 {
		wrapped := fmt.Errorf("tablet snapshot tar produced 0 bytes for snapshot %s", snapshotID)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.tablets_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.tablets_tarred",
		slog.String("snapshot_id", snapshotID),
		slog.Int64("bytes", info.Size()),
	)

	return nil
}

// writeYBSnapshotManifest validates structure and required artifact declarations
// before publishing a manifest. Invalid derived identifiers fail at export time
// rather than creating an export that every reader must reject.
//
// Required artifacts remain separate from decode validation. Archive and cleanup
// discovery must still decode older runs without roles artifacts to skip them
// individually instead of aborting an entire search.
func writeYBSnapshotManifest(ctx context.Context, path string, manifest ybSnapshotManifest) error {
	logger := telemetry.L(ctx)
	if err := manifest.validate(); err != nil {
		wrapped := fmt.Errorf("yb snapshot manifest not writable: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.manifest_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if undeclared := undeclaredYBRequiredArtifacts(manifest); len(undeclared) > 0 {
		wrapped := fmt.Errorf(
			"yb snapshot manifest does not declare required artifacts %s; the run would restore into an unusable database",
			strings.Join(undeclared, ", "),
		)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.manifest_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		wrapped := fmt.Errorf("marshal yb snapshot manifest: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.manifest_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if err := os.WriteFile(path, body, 0o600); err != nil {
		wrapped := fmt.Errorf("write yb snapshot manifest %s: %w", path, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.manifest_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// ybSnapshotKeyPrefix returns the root for orchestrator and node artifacts.
func ybSnapshotKeyPrefix(runID string) string {
	return ybSnapshotRootPrefix + runID + "/"
}

// putYBSnapshotObject uploads a staged file. Tests substitute an upload-order
// recorder without requiring an object store.
var putYBSnapshotObject = putObjectFromFile

// ybSnapshotArtifact pairs a staged path with its uploaded object basename.
type ybSnapshotArtifact struct {
	name string
	path string
}

// ybSnapshotUploadArtifacts orders root artifacts before the manifest. Readers
// require that manifest only after every declared artifact exists.
func ybSnapshotUploadArtifacts(
	stageDir string,
	schemaPath string,
	rolesPath string,
	manifestPath string,
) []ybSnapshotArtifact {
	return []ybSnapshotArtifact{
		{name: ybSnapshotMetadataObject, path: filepath.Join(stageDir, ybSnapshotMetadataObject)},
		{name: ybSnapshotSchemaObject, path: schemaPath},
		{name: ybSnapshotRolesObject, path: rolesPath},
		{name: ybSnapshotManifestObject, path: manifestPath},
	}
}

// ybSnapshotGatedArtifactNames derives required artifacts from uploads, except
// the manifest itself. Discovery already requires its presence.
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

// ybNodeUploadArtifacts uses declared node upload order and matching staged
// basenames. The archive uploads after its required inventory.
func ybNodeUploadArtifacts(stageDir string) []ybSnapshotArtifact {
	objects := ybNodeArtifactObjects()

	files := make([]ybSnapshotArtifact, 0, len(objects))
	for _, object := range objects {
		files = append(
			files,
			ybSnapshotArtifact{name: object, path: filepath.Join(stageDir, object)},
		)
	}

	return files
}

// uploadYBSnapshotArtifacts preserves slice order and stops at the first
// failure. Uploading the completeness object last prevents a partial upload
// from publishing a manifest without artifacts or an archive without inventory.
func uploadYBSnapshotArtifacts(
	ctx context.Context,
	cfg *config.Config,
	prefix string,
	files []ybSnapshotArtifact,
) error {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)

	for _, file := range files {
		if err := putYBSnapshotObject(
			ctx,
			s3Client,
			cfg.BackupS3BucketMain,
			prefix+file.name,
			file.path,
		); err != nil {
			return err
		}
	}

	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.uploaded",
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("prefix", prefix),
		slog.Int("files", len(files)),
	)

	return nil
}
