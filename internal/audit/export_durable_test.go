// These tests check export durability ordering without simulating power loss.
// An export must stop at a failed durability barrier before publishing a
// manifest or reclaiming the previous bundle's rows.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAnExportRefusesToPublishRowsItCouldNotMakeDurable checks that row
// persistence precedes manifest publication and removal of the previous rows.
// Before the durability fix, power loss in that interval left a signed manifest
// without its rows after reclamation removed the previous rows.
//
// The test renames the directory while the source supplies the last row. The
// directory sync must fail and stop the export before publication. Earlier code
// only flushed and closed the rows file, then failed during publication with
// "audit export write manifest". The assertion requires the earlier directory
// sync failure.
func TestAnExportRefusesToPublishRowsItCouldNotMakeDurable(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	moved := filepath.Join(parent, "bundle-moved")
	orgID := uuid.Must(uuid.NewV7())
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const rows = 30
	takeTheDirectoryAway := func(index int) {
		if index != rows-1 {
			return
		}
		if renameErr := os.Rename(dir, moved); renameErr != nil {
			t.Errorf("take the bundle directory away: %v", renameErr)
		}
	}

	manifest, err := Export(context.Background(), exportTestRows(t, orgID, rows, takeTheDirectoryAway),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err == nil {
		t.Fatalf("the export reported success for rows it could not make durable: %+v", manifest)
	}
	if !strings.Contains(err.Error(), "sync dir") {
		t.Fatalf("err = %v, want the export stopped at the durability barrier before publishing", err)
	}

	// The export must not publish a manifest for rows without a durable directory
	// entry.
	if _, statErr := os.Stat(filepath.Join(moved, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest for rows it could not make durable")
	}
	if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest into a directory it had already lost")
	}
}

// TestAnExportPublishesRowsAndManifestThatBothSurvive checks a successful
// export with the durability barriers on the test filesystem. A filesystem that
// rejects directory sync could fail an otherwise complete export.
//
// The test verifies the resulting bundle on that filesystem. It checks that the
// barriers permit the export; it does not simulate power loss.
func TestAnExportPublishesRowsAndManifestThatBothSurvive(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	manifest, err := Export(context.Background(), exportTestRows(t, orgID, 50, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("the durability barriers failed the export on this filesystem: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, manifest.EventsFile)); statErr != nil {
		t.Fatalf("the published rows are missing: %v", statErr)
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle written through the durability barriers does not verify: %v", verdict)
	}
}
