// backup_restore_drill_yb_stage.go brings one export run's artifacts down to
// the drill host and hands the scratch container the same files through a
// read-only bind mount. The run root contributes every artifact the manifest
// declares, and every node contributes two: the tablet archive and the
// inventory of what that archive carries, which the drill reads here so it
// knows what each node's extraction owes before it copies anything.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybDrillArtifactMount is where the staging directory appears inside the
	// scratch container.
	ybDrillArtifactMount = "/artifacts"
	// The staged name of one node's tablet archive is built around the node
	// name from these two halves, so the extraction program can name the same
	// file with the node as a positional parameter rather than as program text.
	ybDrillArchivePrefix = "tablets-"
	ybDrillArchiveSuffix = ".tar.gz"
	// ybDrillInventoryPrefix opens the staged name of one node's inventory.
	ybDrillInventoryPrefix = "inventory-"
)

// ybDrillArchiveName is the staged file name of one node's tablet archive.
func ybDrillArchiveName(node string) string {
	return ybDrillArchivePrefix + node + ybDrillArchiveSuffix
}

// ybDrillInventoryName is the staged file name of one node's inventory.
func ybDrillInventoryName(node string) string {
	return ybDrillInventoryPrefix + node
}

// ybDrillArtifactPath spells a staged file the way the scratch container sees
// it through the bind mount. Every fixed path the restore opens is built here
// from the artifact's own object name, so the files the restore reads and the
// names ybRequiredRunArtifacts makes the manifest declare cannot drift apart.
func ybDrillArtifactPath(name string) string {
	return ybDrillArtifactMount + "/" + name
}

// ybTabletExtractScript unpacks one node's staged archive into that node's own
// directory under the export root, so copies of the same tablet from different
// nodes never mix files. The node name arrives as the shell's first positional
// parameter, so a manifest-supplied name can never be parsed as shell syntax;
// manifest decode allowlists the names as well. The export root is the
// layout's, the same one the inventory check and the placement read, so the
// three cannot drift apart. artifactDir is where the staged archives are
// readable from, which is the bind mount in the scratch container.
func ybTabletExtractScript(exportRoot, artifactDir string) string {
	nodeDir := shellQuote(exportRoot) + `/"$1"`
	archive := shellQuote(artifactDir) + `/"` + ybDrillArchivePrefix + `$1` + ybDrillArchiveSuffix + `"`
	return "mkdir -p " + nodeDir + " && tar xzf " + archive + " -C " + nodeDir
}

// stageYBDrillArtifacts downloads every run-root artifact the manifest declares
// and every node's archive and inventory into stageDir, naming each archive for
// its node so the scratch container can extract each node's files into its own
// directory. The run-root artifacts come from the manifest rather than a list
// held here, so the drill stages whatever the export published, and the
// manifest is refused before this runs unless it declares everything the
// restore opens by name. It returns the inventories in manifest order, which is
// the order the placement prefers replicas in.
func stageYBDrillArtifacts(
	ctx context.Context,
	r *restoreDrillCtx,
	s3Client *s3.Client,
	manifest ybSnapshotManifest,
	stageDir string,
) ([]ybArchiveInventory, error) {
	for _, name := range manifest.Artifacts {
		if err := getObjectToFile(ctx, s3Client, r.Cfg.BackupS3BucketMain,
			ybRunArtifactKey(manifest.RunID, name), filepath.Join(stageDir, name)); err != nil {
			return nil, err
		}
	}
	inventories := make([]ybArchiveInventory, 0, len(manifest.Nodes))
	for _, node := range manifest.Nodes {
		archive := filepath.Join(stageDir, ybDrillArchiveName(node.Name))
		if err := getObjectToFile(ctx, s3Client, r.Cfg.BackupS3BucketMain,
			ybNodeArchiveKey(manifest.RunID, node), archive); err != nil {
			return nil, err
		}
		inventory, err := stageYBNodeInventory(ctx, r, s3Client, manifest, node, stageDir)
		if err != nil {
			return nil, err
		}
		inventories = append(inventories, inventory)
	}
	return inventories, nil
}

// stageYBNodeInventory downloads one node's inventory and reads it back. The
// read is where a truncated or foreign inventory is caught: it is the record
// every tablet of that node is judged against, so an inventory expecting less
// than the archive carried would quietly weaken the check it exists to make.
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
	if err := getObjectToFile(ctx, s3Client, r.Cfg.BackupS3BucketMain,
		ybNodeInventoryKey(manifest.RunID, node), path); err != nil {
		return empty, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		wrapped := fmt.Errorf("read the staged tablet inventory %s: %w", path, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return empty, wrapped
	}
	inventory, err := parseYBArchiveInventory(manifest.RunID, node.Name, body)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return empty, err
	}
	return inventory, nil
}
