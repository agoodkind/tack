// The staging tests check that manifest artifact names create files directly
// inside the staging directory and that selection rejects references
// outside the export run. The manifest comes from the object store, and its
// names form untrusted input. Staging joins names to a directory that the
// drill bind-mounts and later deletes recursively. The byte-class sweep in
// backup_restore_drill_yb_artifact_names_test.go checks that every accepted
// name is a bare basename.

package ops

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// ybStageStoredRunID identifies the export run that staging tests store in the
// object store. Its run-key timestamp format satisfies manifest validation.
const ybStageStoredRunID = "20260830T030000Z"

// newYBDrillStage returns a drill context, a temporary root, and a staging
// directory inside that root. Tests can distinguish staged files from files
// written elsewhere under the root.
func newYBDrillStage(t *testing.T, cfg *config.Config) (*restoreDrillCtx, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg.BackupRoot = filepath.Join(root, "backup")
	stageDir := filepath.Join(cfg.BackupRoot, "restore-drill-yb-stage")
	if err := os.MkdirAll(stageDir, 0o777); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	return &restoreDrillCtx{
		Cfg:            cfg,
		Cli:            nil,
		RunID:          "stage",
		YBPass:         "",
		YBRunKey:       "",
		FDBTargetTime:  nil,
		containerNames: nil,
		scratchImage:   "",
	}, root, stageDir
}

// filesUnder returns sorted paths relative to root with slash separators.
// It visits every nondirectory entry beneath root. A file outside staging has a
// relative path without the staging prefix.
func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		found = append(found, filepath.ToSlash(relative))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	slices.Sort(found)
	return found
}

// TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage runs
// real staging with the real object-store client. It checks that the created
// files match all declared run-root artifacts and one archive and inventory
// per node, directly under staging. This connects artifact-name validation
// with production path construction; changing that construction would fail the
// assertion.
func TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage(t *testing.T) {
	manifest := newYBSnapshotManifest(ybStageStoredRunID, "snap-1", "tack",
		[]string{"yb1", "yb2"}, ybTestArtifactNames())
	store := newBackupTestStore(t, ybExportRunObjects(t, ybStageStoredRunID, manifest))
	s3Client := store.client
	drill, root, stageDir := newYBDrillStage(t, store.config())

	inventories, err := stageYBDrillArtifacts(context.Background(), drill, s3Client, manifest, stageDir)
	if err != nil {
		t.Fatalf("stageYBDrillArtifacts: %v", err)
	}
	nodeNames := make([]string, 0, len(inventories))
	for _, inventory := range inventories {
		if inventory.RunID != ybStageStoredRunID {
			t.Fatalf("inventory for %s was read for run %q, want %q", inventory.Node, inventory.RunID, ybStageStoredRunID)
		}
		nodeNames = append(nodeNames, inventory.Node)
	}
	if !reflect.DeepEqual(nodeNames, []string{"yb1", "yb2"}) {
		t.Fatalf("inventoried nodes = %v, want [yb1 yb2]", nodeNames)
	}
	stageRelative, err := filepath.Rel(root, stageDir)
	if err != nil {
		t.Fatalf("relative stage: %v", err)
	}
	want := []string{
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotMetadataObject)),
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotRolesObject)),
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotSchemaObject)),
		filepath.ToSlash(filepath.Join(stageRelative, ybDrillArchiveName("yb1"))),
		filepath.ToSlash(filepath.Join(stageRelative, ybDrillInventoryName("yb1"))),
		filepath.ToSlash(filepath.Join(stageRelative, ybDrillArchiveName("yb2"))),
		filepath.ToSlash(filepath.Join(stageRelative, ybDrillInventoryName("yb2"))),
	}
	slices.Sort(want)
	if got := filesUnder(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("staged files:\n got=%v\nwant=%v", got, want)
	}
}

// TestYBDrillWritesNothingOutsideTheStageForAClimbingArtifactName stores an
// export with an artifact name above the staging directory and runs production
// selection. SeaweedFS rejects keys containing "..". The fixture stores the
// object at the cleaned key that an unsafe downloader could request.
//
// Selection must reject the run because its manifest refers outside that run.
// Regardless of the selection result, the temporary root must not contain a
// file outside staging.
func TestYBDrillWritesNothingOutsideTheStageForAClimbingArtifactName(t *testing.T) {
	const climbing = "../../escaped.txt"
	manifest := newYBSnapshotManifest(ybStageStoredRunID, "snap-1", "tack",
		[]string{"yb1"}, append(ybTestArtifactNames(), climbing))
	objects := ybExportRunObjects(t, ybStageStoredRunID, manifest)
	climbingKey := ybSnapshotKeyPrefix(ybStageStoredRunID) + climbing
	objects[path.Clean(climbingKey)] = objects[climbingKey]
	delete(objects, climbingKey)
	store := newBackupTestStore(t, objects)
	s3Client := store.client
	drill, root, stageDir := newYBDrillStage(t, store.config())
	drill.YBRunKey = ybStageStoredRunID

	ctx := context.Background()
	resolved, err := resolveYBDrillExport(ctx, drill, s3Client)
	if err == nil {
		_, stageErr := stageYBDrillArtifacts(ctx, drill, s3Client, resolved, stageDir)
		t.Errorf("a manifest declaring %q must not be drilled; staging returned %v", climbing, stageErr)
	}
	if got := filesUnder(t, root); len(got) > 0 {
		t.Fatalf("the drill wrote %v; a declared name must not put a file anywhere in %s", got, root)
	}
}
