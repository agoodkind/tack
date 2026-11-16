// Staging tests verify direct-child artifact paths and reject untrusted names
// outside the declared run. The scratch container later reads this directory
// through a bind and cleanup recursively deletes it, making containment a
// security requirement.

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

// ybStageStoredRunID supplies a timestamp run ID accepted by manifest validation.
const ybStageStoredRunID = "20260830T030000Z"

// newYBDrillStage nests staging under a temporary root to detect writes outside
// the permitted directory.
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

// filesUnder returns sorted relative paths for regular files under root,
// including files outside the expected staging subtree.
func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	walkErr := filepath.WalkDir(
		root,
		func(path string, entry fs.DirEntry, err error) error {
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
		},
	)
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}

	slices.Sort(found)

	return found
}

// TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage checks
// exact staged root artifacts and per-node archive and inventory files through
// the production object-store client, including inventory run and node order.
func TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage(t *testing.T) {
	manifest := newYBSnapshotManifest(
		ybStageStoredRunID,
		"snap-1",
		"tack",
		[]string{"yb1", "yb2"},
		ybTestArtifactNames(),
	)
	store := newBackupTestStore(t, ybExportRunObjects(t, ybStageStoredRunID, manifest))
	s3Client := store.client
	drill, root, stageDir := newYBDrillStage(t, store.config())

	inventories, err := stageYBDrillArtifacts(
		context.Background(),
		drill,
		s3Client,
		manifest,
		stageDir,
	)
	if err != nil {
		t.Fatalf("stageYBDrillArtifacts: %v", err)
	}

	nodeNames := make([]string, 0, len(inventories))
	for _, inventory := range inventories {
		if inventory.RunID != ybStageStoredRunID {
			t.Fatalf(
				"inventory for %s identifies run %q, want %q",
				inventory.Node,
				inventory.RunID,
				ybStageStoredRunID,
			)
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

// TestYBDrillRejectsTraversalArtifactBeforeStaging checks both run rejection
// and absence of writes outside staging. SeaweedFS rejects literal .. keys;
// the fixture stores the cleaned key that an unsafe downloader could request.
func TestYBDrillRejectsTraversalArtifactBeforeStaging(t *testing.T) {
	const climbing = "../../escaped.txt"
	manifest := newYBSnapshotManifest(
		ybStageStoredRunID,
		"snap-1",
		"tack",
		[]string{"yb1"},
		append(ybTestArtifactNames(), climbing),
	)
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
		t.Errorf(
			"a manifest declaring %q must fail selection; staging returned %v",
			climbing,
			stageErr,
		)
	}

	if got := filesUnder(t, root); len(got) > 0 {
		t.Fatalf(
			"the drill wrote %v; rejected artifact names must not create files in %s",
			got,
			root,
		)
	}
}
