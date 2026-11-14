// export_durable_test.go checks synchronization order before publication and
// verifies successful exports on the test filesystem. It does not simulate power loss.

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

// TestExportRejectsDirectorySyncFailureBeforePublication renames the directory
// during the last visitor call. The rows directory sync must fail before the
// manifest write, preserving the required durability order.
func TestExportRejectsDirectorySyncFailureBeforePublication(t *testing.T) {
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

	manifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, rows, takeTheDirectoryAway),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err == nil {
		t.Fatalf("the export reported success for rows it could not make durable: %+v", manifest)
	}

	if !strings.Contains(err.Error(), "sync dir") {
		t.Fatalf(
			"err = %v, want the export stopped at the durability barrier before publishing",
			err,
		)
	}

	// Publication must not occur after the rows directory sync failed.
	if _, statErr := os.Stat(filepath.Join(moved, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest for rows it could not make durable")
	}

	if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
		t.Fatal("the export published a manifest into a directory it had already lost")
	}
}

// TestExportPublishesVerifiableBundleAfterSync checks that synchronization
// succeeds on this filesystem and produces readable rows with a valid bundle verdict.
func TestExportPublishesVerifiableBundleAfterSync(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	manifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, 50, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
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
