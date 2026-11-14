// export_manifest_name_test.go checks that manifest file names cannot select
// rows outside the bundle directory.

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAManifestCannotNameAFileOutsideTheBundle checks path rejection before
// the verifier reports a signature verdict. Untrusted manifests must not
// select arbitrary files for reading.
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
				// Populate candidate paths with valid rows. Rejection must depend on
				// the invalid file name rather than a missing target.
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
