// backup_restore_drill_yb_staging_test.go proves the property the drill's
// staging step rests on: an artifact name the manifest declares can only ever
// resolve to a file directly inside the staging directory. The manifest is
// fetched from the object store, so those names are untrusted input, and the
// staging step joins each one onto a directory the drill bind-mounts into a
// container and later removes recursively.
//
// The property is established rather than pattern-matched for. The name is
// vetted once at the fetch boundary, and what the sweep below proves is that
// every name that boundary admits is a bare basename, so the join has no
// escaping name left to build a path from.

package ops

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// ybStageStoredRunID is the export run the staging tests plant in the fake
// object store. It is a run-key timestamp because nothing looser survives the
// manifest boundary.
const ybStageStoredRunID = "20260830T030000Z"

// newYBDrillStage returns a drill context, a throwaway root, and the staging
// directory nested inside it, so a test can tell a file written into the stage
// from one written anywhere else.
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
		volumeNames:    nil,
	}, root, stageDir
}

// filesUnder returns every regular file below root, as slash-separated paths
// relative to root, sorted. A file the drill wrote outside the stage shows up
// here as a path that does not start with the stage's own relative path.
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

// TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage drives
// the real staging step against the real object-store client and proves the
// files it creates are exactly the declared artifacts and one archive per node,
// each a direct child of the staging directory. This is what ties the sweep
// below to the path the production code actually builds: a staging step that
// stopped joining names this way would fail here.
func TestStageYBDrillArtifactsWritesEachDeclaredNameDirectlyUnderTheStage(t *testing.T) {
	manifest := newYBSnapshotManifest(ybStageStoredRunID, "snap-1", "tack",
		[]string{"yb1", "yb2"}, ybTestArtifactNames())
	s3Client, cfg := newFakeBackupObjectStore(t, "tack-backups",
		fakeYBExportRunObjects(t, ybStageStoredRunID, manifest))
	drill, root, stageDir := newYBDrillStage(t, cfg)

	nodeNames, err := stageYBDrillArtifacts(context.Background(), drill, s3Client, manifest, stageDir)
	if err != nil {
		t.Fatalf("stageYBDrillArtifacts: %v", err)
	}
	if !reflect.DeepEqual(nodeNames, []string{"yb1", "yb2"}) {
		t.Fatalf("node names = %v, want [yb1 yb2]", nodeNames)
	}
	stageRelative, err := filepath.Rel(root, stageDir)
	if err != nil {
		t.Fatalf("relative stage: %v", err)
	}
	want := []string{
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotMetadataObject)),
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotRolesObject)),
		filepath.ToSlash(filepath.Join(stageRelative, ybSnapshotSchemaObject)),
		filepath.ToSlash(filepath.Join(stageRelative, "tablets-yb1.tar.gz")),
		filepath.ToSlash(filepath.Join(stageRelative, "tablets-yb2.tar.gz")),
	}
	slices.Sort(want)
	if got := filesUnder(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("staged files:\n got=%v\nwant=%v", got, want)
	}
}

// TestYBDrillWritesNothingOutsideTheStageForAClimbingArtifactName plants an
// export whose manifest declares an artifact name that points above the staging
// directory, with the object behind that name present so a download would
// succeed, and drives the real selection the drill runs.
//
// Two things must hold, and the second is the security property. The run must
// not be drilled at all, because a manifest naming something outside its own
// run describes an export nothing should restore from. And whatever the
// selection decides, the throwaway root must hold no file outside the stage.
func TestYBDrillWritesNothingOutsideTheStageForAClimbingArtifactName(t *testing.T) {
	const climbing = "../../escaped.txt"
	manifest := newYBSnapshotManifest(ybStageStoredRunID, "snap-1", "tack",
		[]string{"yb1"}, append(ybTestArtifactNames(), climbing))
	s3Client, cfg := newFakeBackupObjectStore(t, "tack-backups",
		fakeYBExportRunObjects(t, ybStageStoredRunID, manifest))
	drill, root, stageDir := newYBDrillStage(t, cfg)
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

// TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage is the
// containment proof. It enumerates every byte in every position of the short
// names and asserts, for each one the boundary admits, that the path the
// staging step builds is a direct child of the staging directory and that the
// object key stays inside the run's own prefix.
//
// This is a property over the admitted set rather than a check for the shapes
// someone thought of: no name is singled out, and a boundary relaxed to admit a
// separator or a traversal segment fails here without anyone having to predict
// which one. Three bytes is the shortest name that can carry a leading
// character plus a traversal segment, which is the escape the join permits.
// [ybSnapshotManifest.validate] runs this same check on every declared name, and
// the crafted-manifest test above covers that wiring end to end.
func TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage(t *testing.T) {
	const stageDir = "/backup/restore-drill-yb-stage"
	keyPrefix := ybSnapshotKeyPrefix(ybStageStoredRunID)
	admitted := 0
	for first := range 256 {
		for second := range 256 {
			for third := range 256 {
				name := string([]byte{byte(first), byte(second), byte(third)})
				if validateYBArtifactName(name) != nil {
					continue
				}
				admitted++
				if staged := filepath.Join(stageDir, name); filepath.Dir(staged) != stageDir {
					t.Fatalf("admitted name %q stages at %q, outside %q", name, staged, stageDir)
				}
				if key := keyPrefix + name; filepath.Dir(key) != filepath.Clean(keyPrefix) {
					t.Fatalf("admitted name %q keys at %q, outside %q", name, key, keyPrefix)
				}
			}
		}
	}
	if admitted == 0 {
		t.Fatal("the sweep admitted no name at all, so it proved nothing")
	}
	t.Logf("admitted %d of %d three-byte names, every one a direct child", admitted, 256*256*256)
}
