// Artifact staging downloads manifest-declared root files and every node's
// archive and inventory. The scratch container reads them through a read-only
// bind. Inventory validation establishes expected files before placement.

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
	// ybDrillArtifactMount specifies the scratch staging bind path.
	ybDrillArtifactMount = "/artifacts"
	// Archive basenames include the node name between these fixed parts.
	// Extraction supplies the node as a shell parameter, not program text.
	ybDrillArchivePrefix = "tablets-"
	ybDrillArchiveSuffix = ".tar.gz"
	// ybDrillInventoryPrefix starts each staged node inventory basename.
	ybDrillInventoryPrefix = "inventory-"
)

// ybDrillArchiveName returns a node's staged archive basename.
func ybDrillArchiveName(node string) string {
	return ybDrillArchivePrefix + node + ybDrillArchiveSuffix
}

// ybDrillInventoryName returns a node's staged inventory basename.
func ybDrillInventoryName(node string) string {
	return ybDrillInventoryPrefix + node
}

// ybDrillArtifactPath derives scratch read paths from artifact object names,
// matching the required artifact declarations.
func ybDrillArtifactPath(name string) string {
	return ybDrillArtifactMount + "/" + name
}

// ybTabletExtractScript extracts each node separately to avoid mixing replica
// files. Positional node parameters prevent parsing manifest-supplied values
// as shell syntax; decode also validates node names. Comparison and placement
// use this shared export root. artifactDir specifies the staged archive bind.
func ybTabletExtractScript(exportRoot, artifactDir string) string {
	nodeDir := shellQuote(exportRoot) + `/"$1"`
	archive := shellQuote(
		artifactDir,
	) + `/"` + ybDrillArchivePrefix + `$1` + ybDrillArchiveSuffix + `"`

	return "mkdir -p " + nodeDir + " && tar xzf " + archive + " -C " + nodeDir
}

// stageYBDrillArtifacts downloads declared root artifacts and per-node archives
// and inventories under node-specific basenames. Prior manifest validation
// requires every artifact the restore opens. Returned inventory order matches
// manifest order, which controls replica preference.
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

// stageYBNodeInventory downloads and validates a node's recorded file set.
// Truncated inventories or records from another run cannot establish complete
// extraction and must fail before tablet placement.
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
