// Artifact validation must restrict staged files to direct children of the
// staging directory and object prefix. Staging tests exercise the production
// path construction with validated manifest artifact names.

package ops

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage checks
// every byte in first and subsequent positions, plus traversal segments.
// Validation classifies each byte independently and rejects dot pairs anywhere.
// These cases cover separators and traversal without enumerating longer names.
func TestEveryArtifactNameTheManifestBoundaryAdmitsStagesInsideTheStage(t *testing.T) {
	const stageDir = "/backup/restore-drill-yb-stage"
	keyPrefix := ybSnapshotKeyPrefix(ybStageStoredRunID)

	assertContained := func(name string) {
		t.Helper()
		if staged := filepath.Join(stageDir, name); filepath.Dir(staged) != stageDir {
			t.Fatalf("admitted name %q stages at %q, outside %q", name, staged, stageDir)
		}

		if key := keyPrefix + name; filepath.Dir(key) != filepath.Clean(keyPrefix) {
			t.Fatalf("admitted name %q creates key %q, outside %q", name, key, keyPrefix)
		}
	}

	// Each byte appears alone in the first position and after an accepted
	// first byte in the subsequent position.
	admitted := 0

	for b := range 256 {
		for _, name := range []string{string([]byte{byte(b)}), "a" + string([]byte{byte(b)})} {
			if validateYBArtifactName(name) != nil {
				continue
			}

			admitted++
			if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
				t.Fatalf("the boundary admitted %q, which contains a separator or NUL", name)
			}

			assertContained(name)
		}
	}

	if admitted == 0 {
		t.Fatal("the sweep did not admit any name and cannot establish containment")
	}

	// Validation rejects dot pairs anywhere, not only standalone traversal
	// segments. Single dots remain valid within direct-child artifact names.
	for _, refused := range []string{".", "..", "a..", "a..b", "..a"} {
		if validateYBArtifactName(refused) == nil {
			t.Fatalf("the boundary admitted %q, which contains a traversal segment", refused)
		}
	}

	for _, name := range []string{"a.b", "a.", "a.b.c", "a-b", "a_b", "schema.sql"} {
		if err := validateYBArtifactName(name); err != nil {
			t.Fatalf("expected one-component name %q to pass validation: %v", name, err)
		}

		assertContained(name)
	}

	t.Logf("admitted %d single-byte and two-byte names, every one a direct child", admitted)
}
