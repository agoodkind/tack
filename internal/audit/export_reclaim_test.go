// These tests check verification after cleanup deletes the selected rows file.
// Readers do not acquire the export activity lock, and deletion can occur
// before a reader opens the file.

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

// TestAVerifyWhoseRowsWereReclaimedFailsRatherThanPassing checks that
// verification reports a missing rows file. Readers do not acquire the activity
// lock, and cleanup can delete rows between the manifest read and the file
// open. This lock policy requires an explicit open failure.
//
// Treating a missing file as empty could incorrectly accept a zero-row bundle:
// the declared count, empty-file digest, and signature would agree. A nonempty
// bundle would also fail the count check and would not isolate the missing-file
// check. The test covers both sizes.
func TestAVerifyWhoseRowsWereReclaimedFailsRatherThanPassing(t *testing.T) {
	sizes := []struct {
		name string
		rows int
	}{
		{name: "a bundle that declares rows", rows: 25},
		{name: "a bundle that declares none", rows: 0},
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

				// Delete the selected rows as cleanup does after another export supersedes the
				// bundle. This reproduces deletion before the reader opens the rows.
				if err := os.Remove(filepath.Join(dir, manifest.EventsFile)); err != nil {
					t.Fatalf("free the rows the manifest names: %v", err)
				}

				report, err := VerifyBundle(dir, publicKey)
				if err == nil {
					t.Fatalf("the verifier reported on a bundle whose rows are gone: %+v", report)
				}
			},
		)
	}
}
