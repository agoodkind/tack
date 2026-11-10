// export_manifest_name_test.go covers name validation: the events file name a
// manifest carries is untrusted input, and a name that points outside the
// bundle is refused rather than followed.

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAManifestCannotNameAFileOutsideTheBundle pins that the manifest is
// untrusted input. The scan opens the file the manifest names before the
// signature verdict is reported, so a manifest able to name any path would make
// the verifier read whatever a foreign bundle chose to point it at.
func TestAManifestCannotNameAFileOutsideTheBundle(t *testing.T) {
	for _, name := range []string{"../events.jsonl", "sub/events.jsonl", "/etc/passwd", "..", "."} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "bundle")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			rows := chainedExportTestRows(t, 2)
			pub := writeSignedExportTestBundle(t, dir, rows)
			// A decoy at each reachable target, so a verifier that followed the
			// name would find something to scan rather than fail on an absent file.
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
		})
	}
}
