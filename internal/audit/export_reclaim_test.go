// export_reclaim_test.go checks verification failures after reclamation removes
// rows between a manifest read and a rows open. Readers do not acquire export locks.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// TestVerifyBundleRejectsReclaimedRowsFile requires a missing
// rows file to fail even for a signed empty bundle. Treating a missing file as
// empty would preserve that bundle's count and digest and hide reclamation.
func TestVerifyBundleRejectsReclaimedRowsFile(t *testing.T) {
	sizes := []struct {
		name string
		rows int
	}{
		{name: "verifier rejects missing nonempty rows file", rows: 25},
		{name: "verifier rejects missing empty rows file", rows: 0},
	}
	for _, size := range sizes {
		t.Run(
			size.name,
			func(t *testing.T) {
				dir := t.TempDir()
				orgID := uuid.Must(uuid.NewV7())
				publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatalf("generate key: %v", err)
				}

				manifest, err := Export(
					context.Background(),
					exportTestRows(t, orgID, size.rows, nil),
					privateKey,
					"ed25519:test",
					exportTestFilter(orgID),
					dir,
				)
				if err != nil {
					t.Fatalf("export: %v", err)
				}

				if manifest.RowCount != size.rows {
					t.Fatalf("row count = %d, want %d", manifest.RowCount, size.rows)
				}

				// Remove the superseded rows as reclamation would before a reader opens them.
				if err := os.Remove(filepath.Join(dir, manifest.EventsFile)); err != nil {
					t.Fatalf("free the rows the manifest names: %v", err)
				}

				report, err := VerifyBundle(dir, publicKey)
				if err == nil {
					t.Fatalf("the verifier accepted a bundle after its rows disappeared: %+v", report)
				}
			},
		)
	}
}
