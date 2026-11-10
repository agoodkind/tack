// export_manifest_binding_test.go covers the binding between a manifest and the
// rows it names: the name is inside the signature, so neither repointing a
// manifest at another export's rows nor adding or stripping its name survives
// verification.

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

// TestAManifestRepointedAtAnotherExportsRowsFailsItsSignature is the reason the
// events file name has to be signed. Verification opens the file the manifest
// names, so an unsigned name would let anyone holding a genuine signed manifest
// present it over a different export's rows and have the pair read as a bundle.
// The name is inside what the signature covers, so the repointed manifest is
// refused on the signature, before any question of what the rows contain.
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

// TestNamingItsOwnRowsIsPartOfWhatAManifestSigns pins the binding in both
// directions on a bundle nothing else is wrong with. The name added below
// resolves to the very file the manifest already described, so the rows, the
// digest, and the row count all still agree; only the signature can fail, and
// it must, because the name is covered whether it is present or absent.
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
		// The rows are placed under the name the fallback resolves to as well, so
		// stripping the name leaves the digest and the count intact and the
		// signature alone decides.
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
