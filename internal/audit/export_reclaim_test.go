// export_reclaim_test.go covers what a reader sees when a reclaim frees the
// rows it was about to read. Readers do not hold the export beacon, so that
// window is real and the decision not to hold it depends on what happens in it.

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

// TestAVerifyWhoseRowsWereReclaimedFailsRatherThanPassing pins the outcome the
// reader-locking decision rests on. Readers take no lock, so a verify can lose
// its rows between reading the manifest and opening them, and the whole case for
// not locking is that this failure is loud. It has to stay loud.
//
// The regression that would make it quiet is treating an absent rows file as an
// empty one. The zero-row bundle is where that goes unnoticed: the manifest
// declares no rows, the digest of nothing matches, the signature is valid, and
// the report comes back clean for a bundle whose rows are not there. A bundle
// that declares rows would also trip the count comparison, so on its own it
// cannot show that the open is what refused.
func TestAVerifyWhoseRowsWereReclaimedFailsRatherThanPassing(t *testing.T) {
	sizes := []struct {
		name string
		rows int
	}{
		{name: "a bundle that declares rows", rows: 25},
		{name: "a bundle that declares none", rows: 0},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			dir := t.TempDir()
			orgID := uuid.Must(uuid.NewV7())
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			manifest, err := Export(context.Background(), exportTestRows(t, orgID, size.rows, nil),
				privateKey, "ed25519:test", exportTestFilter(orgID), dir)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if manifest.RowCount != size.rows {
				t.Fatalf("row count = %d, want %d", manifest.RowCount, size.rows)
			}

			// Exactly what a reclaim does to the rows of a bundle it supersedes,
			// applied in the window a reader does not hold.
			if err := os.Remove(filepath.Join(dir, manifest.EventsFile)); err != nil {
				t.Fatalf("free the rows the manifest names: %v", err)
			}

			report, err := VerifyBundle(dir, publicKey)
			if err == nil {
				t.Fatalf("the verifier reported on a bundle whose rows are gone: %+v", report)
			}
		})
	}
}
