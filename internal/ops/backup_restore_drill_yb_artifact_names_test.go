// backup_restore_drill_yb_artifact_names_test.go proves the property the
// drill's staging step rests on: an artifact name the manifest boundary admits
// can only ever resolve to a file directly inside the staging directory.
//
// The property is established rather than pattern-matched for. The name is
// vetted once at the fetch boundary, and what the sweep proves is that every
// name that boundary admits is a bare basename, so the join has no escaping
// name left to build a path from. The end-to-end staging tests in
// backup_restore_drill_yb_staging_test.go tie the sweep to the path the
// production code builds.

package ops

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage is the
// containment proof, and it is complete without enumerating every short name.
//
// A joined path escapes its directory in exactly two ways: a component
// separator inside the name, or a name that is itself a traversal segment. The
// boundary decides admission byte by byte, with one rule for the first byte and
// one for the rest, so sweeping every byte in each of those two positions
// covers every separator any name could carry. The only multi-byte escape, the
// traversal segment, is a dot pair, and the boundary refuses a dot pair
// anywhere in a name. Every admitted name is therefore one component that is
// not a traversal segment, which is a direct child. The join is then checked on
// every admitted byte and on the segments themselves, so a boundary relaxed to
// admit either fails here without anyone having to predict which one.
//
// Enumerating every three-byte name proved the same property at sixteen million
// iterations, which made the suite slow for no added coverage: admission depends
// on a byte's class, not on its neighbours.
//
// [ybSnapshotManifest.validate] runs this same check on every declared name, and
// the crafted-manifest test above covers that wiring end to end.
func TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage(t *testing.T) {
	const stageDir = "/backup/restore-drill-yb-stage"
	keyPrefix := ybSnapshotKeyPrefix(ybStageStoredRunID)

	assertContained := func(name string) {
		t.Helper()
		if staged := filepath.Join(stageDir, name); filepath.Dir(staged) != stageDir {
			t.Fatalf("admitted name %q stages at %q, outside %q", name, staged, stageDir)
		}
		if key := keyPrefix + name; filepath.Dir(key) != filepath.Clean(keyPrefix) {
			t.Fatalf("admitted name %q keys at %q, outside %q", name, key, keyPrefix)
		}
	}

	// Every byte in the first position, alone, and every byte in the rest
	// position, behind a byte the first position admits.
	admitted := 0
	for b := range 256 {
		for _, name := range []string{string([]byte{byte(b)}), "a" + string([]byte{byte(b)})} {
			if validateYBArtifactName(name) != nil {
				continue
			}
			admitted++
			if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
				t.Fatalf("the boundary admitted %q, which carries a separator or NUL", name)
			}
			assertContained(name)
		}
	}
	if admitted == 0 {
		t.Fatal("the sweep admitted no name at all, so it proved nothing")
	}

	// The traversal segments and their near neighbours. The boundary refuses a
	// dot pair anywhere in a name, not only a name that is one, which is
	// stricter than containment needs and is the choice the code made; the
	// names it does admit carry single dots and stage as direct children.
	for _, refused := range []string{".", "..", "a..", "a..b", "..a"} {
		if validateYBArtifactName(refused) == nil {
			t.Fatalf("the boundary admitted %q, which carries a traversal segment", refused)
		}
	}
	for _, name := range []string{"a.b", "a.", "a.b.c", "a-b", "a_b", "schema.sql"} {
		if err := validateYBArtifactName(name); err != nil {
			t.Fatalf("expected %q to be admitted, it is one component: %v", name, err)
		}
		assertContained(name)
	}
	t.Logf("admitted %d single-byte and two-byte names, every one a direct child", admitted)
}
