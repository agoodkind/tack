// These tests check that the verifier rejects an untrusted events filename that
// selects a path outside the bundle.

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAManifestCannotNameAFileOutsideTheBundle checks path restrictions for an
// untrusted manifest. The verifier opens selected rows before reporting the
// signature verdict. An unrestricted path would let a foreign bundle select
// other files for reading.
func TestAManifestCannotNameAFileOutsideTheBundle(t *testing.T) {
	for _, name := range []string{"../events.jsonl", "sub/events.jsonl", "/etc/passwd", "..", "."} {
		t.Run(
			name,
			func(t *testing.T) {
				root := t.TempDir()
				dir := filepath.Join(root, "bundle")
				if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
					t.Fatal(err)
				}

				rows := chainedExportTestRows(t, 2)
				pub := writeSignedExportTestBundle(t, dir, rows)
				// The test creates decoys at the parent and nested test paths. A verifier that
				// follows either path finds data rather than failing on a missing file.
				decoy, err := os.ReadFile(filepath.Join(dir, legacyExportEventsFile))
				if err != nil {
					t.Fatal(err)
				}

				for _, target := range []string{
					filepath.Join(root, legacyExportEventsFile),
					filepath.Join(dir, "sub", legacyExportEventsFile),
				} {
					if err := os.WriteFile(target, decoy, 0o600); err != nil {
						t.Fatal(err)
					}
				}

				outside := name
				rewriteManifestField(t, dir, "events_file", &outside)

				_, err = VerifyBundle(dir, pub)
				if err == nil {
					t.Fatalf("a manifest naming %q was followed", name)
				}

				if !strings.Contains(err.Error(), "not a file name inside the bundle") {
					t.Fatalf("err = %v, want the name refused for leaving the bundle", err)
				}
			},
		)
	}
}
