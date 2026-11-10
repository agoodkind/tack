// The artifact-name test checks that every name the manifest validator admits
// identifies a direct child of the staging directory. The validator checks
// names at the fetch boundary. The byte-class test establishes that accepted
// names remain basenames, without traversal or separators. The staging tests
// exercise the production path construction with crafted manifests.

package ops

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage checks
// containment without enumerating every short name. A separator or traversal
// component would permit a name to identify another directory.
//
// The validator checks first and later bytes independently and rejects dot
// pairs anywhere. Testing every byte in both positions covers separators;
// testing traversal segments covers dot-pair escapes. The test checks the
// joined path for every admitted byte and the traversal cases. A relaxed rule
// that admits either must fail this test.
//
// An earlier three-byte enumeration used sixteen million iterations without
// adding coverage because admission depends on byte class rather than adjacent
// bytes. The current test preserves that coverage with fewer combinations.
//
// [ybSnapshotManifest.validate] applies the validator to every declared name.
// The crafted-manifest staging test checks that integration.
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

	// The test checks every byte alone in the first position and after an accepted
	// first byte in the later position.
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

	// The test checks traversal segments and similar names. The validator rejects
	// dot pairs anywhere, which exceeds the direct-child requirement. Accepted
	// names can contain single dots and must still identify direct children.
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
