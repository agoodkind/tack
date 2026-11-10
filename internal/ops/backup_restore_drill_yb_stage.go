// Artifact staging downloads one export run to the drill host and exposes those
// files to the scratch container through a read-only bind mount. It stages
// every run-root artifact in the manifest and each node's tablet archive and
// inventory. The drill reads inventories before copying tablet files to check
// each extraction against its archive record.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/ops/objectstage"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybDrillArtifactMount specifies the staging directory's mount inside the
	// scratch container.
	ybDrillArtifactMount = "/artifacts"
	// The archive prefix and suffix surround the node name in a staged archive
	// filename. Extraction supplies the node as a positional parameter instead of
	// inserting it into shell program text.
	ybDrillArchivePrefix = "tablets-"
	ybDrillArchiveSuffix = ".tar.gz"
	// ybDrillInventoryPrefix starts the staged inventory filename for each node.
	ybDrillInventoryPrefix = "inventory-"
)

// ybDrillArchiveName returns one node's staged tablet archive filename.
func ybDrillArchiveName(node string) string {
	return ybDrillArchivePrefix + node + ybDrillArchiveSuffix
}

// ybDrillInventoryName returns one node's staged inventory filename.
func ybDrillInventoryName(node string) string {
	return ybDrillInventoryPrefix + node
}

// ybDrillArtifactPath constructs a fixed artifact path under the scratch bind
// mount. Restore uses the artifact object name that ybRequiredRunArtifacts
// requires in the manifest, instead of maintaining an independent path.
func ybDrillArtifactPath(name string) string {
	return ybDrillArtifactMount + "/" + name
}

// ybTabletExtractScript extracts one staged archive into a directory for its
// node under exportRoot. Separate node directories prevent replicas from mixing
// files. The shell reads the node name as its first positional parameter
// instead of parsing it as program syntax. Manifest decoding also checks names
// against an allowed set.
//
// The inventory check and placement use the same export root from the layout.
// artifactDir specifies the readable archive directory, which uses the scratch
// container's bind mount.
func ybTabletExtractScript(exportRoot, artifactDir string) string {
	nodeDir := shellQuote(exportRoot) + `/"$1"`
	archive := shellQuote(
		artifactDir,
	) + `/"` + ybDrillArchivePrefix + `$1` + ybDrillArchiveSuffix + `"`

	return "mkdir -p " + nodeDir + " && tar xzf " + archive + " -C " + nodeDir
}

// stageYBDrillArtifacts downloads manifest run-root artifacts and each node's
// archive and inventory into stageDir. Archive filenames include node names
// for separate extraction directories. The manifest supplies the run-root
// artifact list; selection rejects it before staging unless it includes every
// fixed restore artifact. The returned inventories follow manifest order,
// which determines replica preference.
func stageYBDrillArtifacts(
	ctx context.Context,
	r *restoreDrillCtx,
	s3Client *s3.Client,
	manifest ybSnapshotManifest,
	stageDir string,
) ([]ybArchiveInventory, error) {
	for _, name := range manifest.Artifacts {
		if err := objectstage.GetObjectToFile(
			ctx,
			s3Client,
			r.Cfg.BackupS3BucketMain,
			ybRunArtifactKey(manifest.RunID, name),
			filepath.Join(stageDir, name),
		); err != nil {
			return nil, ybStagingError(ctx, err)
		}
	}

	inventories := make([]ybArchiveInventory, 0, len(manifest.Nodes))
	for _, node := range manifest.Nodes {
		archive := filepath.Join(stageDir, ybDrillArchiveName(node.Name))
		if err := objectstage.GetObjectToFile(
			ctx,
			s3Client,
			r.Cfg.BackupS3BucketMain,
			ybNodeArchiveKey(manifest.RunID, node),
			archive,
		); err != nil {
			return nil, ybStagingError(ctx, err)
		}

		inventory, err := stageYBNodeInventory(ctx, r, s3Client, manifest, node, stageDir)
		if err != nil {
			return nil, err
		}

		inventories = append(inventories, inventory)
	}

	return inventories, nil
}

func ybStagingError(ctx context.Context, err error) error {
	wrapped := fmt.Errorf("stage a drill artifact: %w", err)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"backup.restore_drill.yb.failed",
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}

// stageYBNodeInventory downloads and parses one node's inventory. Parsing
// rejects a truncated inventory or one from another run or node. Tablet
// verification depends on this record; accepting fewer expected files than the
// archive contained would weaken that verification.
func stageYBNodeInventory(
	ctx context.Context,
	r *restoreDrillCtx,
	s3Client *s3.Client,
	manifest ybSnapshotManifest,
	node ybSnapshotManifestNode,
	stageDir string,
) (ybArchiveInventory, error) {
	logger := telemetry.L(ctx)
	empty := ybArchiveInventory{RunID: "", Node: "", Files: nil}
	path := filepath.Join(stageDir, ybDrillInventoryName(node.Name))
	if err := objectstage.GetObjectToFile(
		ctx,
		s3Client,
		r.Cfg.BackupS3BucketMain,
		ybNodeInventoryKey(manifest.RunID, node),
		path,
	); err != nil {
		return empty, ybStagingError(ctx, err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		wrapped := fmt.Errorf("read the staged tablet inventory %s: %w", path, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return empty, wrapped
	}

	inventory, err := parseYBArchiveInventory(manifest.RunID, node.Name, body)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return empty, err
	}

	return inventory, nil
}
