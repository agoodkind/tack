// export_durable_test.go covers the order an export makes its parts durable in.
// A power loss cannot be staged in a test, so what is checked here is the order:
// the export refuses at the durability barrier rather than reaching the steps
// that publish a manifest over the rows and free the rows it replaces.

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

// TestAnExportRefusesToPublishRowsItCouldNotMakeDurable pins that the rows are
// on stable storage before anything acts on them. The two steps that follow the
// rows write are both irreversible against a file that is not on disk: the
// manifest is signed over the rows and published, and the rows of the bundle
// this one replaces are unlinked. A host that lost power in that window came
// back holding a signed manifest naming rows that were never written, with the
// rows it superseded already freed.
//
// The barrier is induced where it is observable: the bundle directory is renamed
// away while the last row is still being handed over, so the directory sync that
// makes the rows' entry durable cannot run. The export must fail there.
//
// This is what fails without the fix. The rows write used to flush, close, and
// return, so the export walked straight on into the publish and failed later at
// the staged manifest write with "audit export write manifest". Reporting the
// durability barrier instead is the whole ordering claim: the export stops
// before it publishes, not while it is publishing.
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

	// Nothing was published. A manifest here would be one signed over rows whose
	// directory entry never reached the disk.
	if _, statErr := os.Stat(filepath.Join(moved, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest for rows it could not make durable")
	}
	if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest into a directory it had already lost")
	}
}

// TestAnExportPublishesRowsAndManifestThatBothSurvive pins that the durability
// barriers do not cost the export its result. Every barrier the fix adds is a
// call that can fail on a filesystem that does not support it, and a directory
// sync that always failed would turn every export into an error while the bundle
// it wrote sat on disk complete.
//
// This runs on whatever filesystem the suite runs on, which is what makes it the
// check that the barriers are usable there rather than only correct in theory.
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
