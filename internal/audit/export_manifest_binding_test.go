// The manifest signature includes its rows filename. These tests check that
// repointing the filename, adding it, or deleting it fails verification.

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

// TestAManifestRepointedAtAnotherExportsRowsFailsItsSignature checks that the
// signature includes the rows filename. Without that binding, a genuine
// manifest could select another export's rows.
//
// The verifier reads the selected rows before reporting the signature verdict.
// The test confirms that it scans the foreign rows and rejects the changed
// manifest signature.
func TestAManifestRepointedAtAnotherExportsRowsFailsItsSignature(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const genuineRows, foreignRows = 30, 70
	dir, foreignDir := t.TempDir(), t.TempDir()
	if _, err := Export(context.Background(), exportTestRows(t, orgID, genuineRows, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
		t.Fatalf("genuine export: %v", err)
	}
	foreign, err := Export(context.Background(), exportTestRows(t, orgID, foreignRows, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), foreignDir)
	if err != nil {
		t.Fatalf("foreign export: %v", err)
	}
	copyBundleRows(t, foreignDir, dir, foreign.EventsFile)
	rewriteManifestField(t, dir, "events_file", &foreign.EventsFile)

	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("VerifyBundle must report on a repointed manifest, not fail outright: %v", err)
	}
	if report.RowsScanned != foreignRows {
		t.Fatalf("rows scanned = %d, want the %d rows the name points at; the manifest's name is what is read",
			report.RowsScanned, foreignRows)
	}
	if report.SignatureOK {
		t.Fatal("a manifest repointed at another export's rows verified, so the name is outside the signature")
	}
	if report.Err() == nil {
		t.Fatal("the repointed bundle passed its verdict")
	}
}

// TestNamingItsOwnRowsIsPartOfWhatAManifestSigns checks additions and deletions
// of EventsFile on otherwise valid bundles. Each changed manifest still selects
// the original rows, preserving their digest and count. The signature must fail
// because both the field's presence and its absence affect the signing bytes.
func TestNamingItsOwnRowsIsPartOfWhatAManifestSigns(t *testing.T) {
	t.Run("a name added to a manifest that had none", func(t *testing.T) {
		dir := t.TempDir()
		pub := writeSignedExportTestBundle(t, dir, chainedExportTestRows(t, 3))
		if report, err := VerifyBundle(dir, pub); err != nil || report.Err() != nil {
			t.Fatalf("the bundle must verify before it is edited: %v %v", err, report.Err())
		}

		sameFile := legacyExportEventsFile
		rewriteManifestField(t, dir, "events_file", &sameFile)

		report, err := VerifyBundle(dir, pub)
		if err != nil {
			t.Fatalf("VerifyBundle: %v", err)
		}
		if !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
			t.Fatalf("digest=%v hashes=%d/%d: every other check must pass so the signature is the one under test",
				report.FileSHA256OK, report.HashMatches, report.RowsScanned)
		}
		if report.SignatureOK {
			t.Fatal("a name added after signing verified, so an unsigned name can decide which rows are read")
		}
	})

	t.Run("a name stripped from a manifest that had one", func(t *testing.T) {
		dir := t.TempDir()
		orgID := uuid.Must(uuid.NewV7())
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		manifest, err := Export(context.Background(), exportTestRows(t, orgID, 20, nil),
			privateKey, "ed25519:test", exportTestFilter(orgID), dir)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		// The test also writes the rows at the legacy fallback path. Deleting
		// EventsFile preserves the digest and count; only the signature determines
		// rejection.
		copyBundleRows(t, dir, dir, manifest.EventsFile)
		rows, err := os.ReadFile(filepath.Join(dir, manifest.EventsFile))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, legacyExportEventsFile), rows, 0o600); err != nil {
			t.Fatal(err)
		}
		rewriteManifestField(t, dir, "events_file", nil)

		report, err := VerifyBundle(dir, publicKey)
		if err != nil {
			t.Fatalf("VerifyBundle: %v", err)
		}
		if !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
			t.Fatalf("digest=%v hashes=%d/%d: every other check must pass so the signature is the one under test",
				report.FileSHA256OK, report.HashMatches, report.RowsScanned)
		}
		if report.SignatureOK {
			t.Fatal("a manifest with its name stripped verified, so the name is outside the signature")
		}
	})
}
