// export_manifest_binding_test.go checks signature coverage of the rows file
// name, including substitution, addition, and removal.

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

// TestVerifyBundleRejectsRowsFileSubstitution changes only the selected rows
// file after signing. Verification must reject the manifest signature.
func TestVerifyBundleRejectsRowsFileSubstitution(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const genuineRows, foreignRows = 30, 70
	dir, foreignDir := t.TempDir(), t.TempDir()
	if _, err := Export(
		context.Background(),
		exportTestRows(t, orgID, genuineRows, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	); err != nil {
		t.Fatalf("genuine export: %v", err)
	}

	foreign, err := Export(
		context.Background(),
		exportTestRows(t, orgID, foreignRows, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		foreignDir,
	)
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
		t.Fatalf(
			"rows scanned = %d, want the %d rows selected by the edited manifest",
			report.RowsScanned,
			foreignRows,
		)
	}

	if report.SignatureOK {
		t.Fatal("the manifest signature accepted a substituted rows file name")
	}

	if report.Err() == nil {
		t.Fatal("the repointed bundle passed its verdict")
	}
}

// TestVerifyBundleRejectsRowsFileFieldChanges checks addition and removal
// while preserving the actual rows, digest, and count to isolate the signature.
func TestVerifyBundleRejectsRowsFileFieldChanges(t *testing.T) {
	t.Run(
		"verifier rejects added file name",
		func(t *testing.T) {
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
				t.Fatalf(
					"digest=%v hashes=%d/%d: digest and row hashes must pass before checking the signature",
					report.FileSHA256OK,
					report.HashMatches,
					report.RowsScanned,
				)
			}

			if report.SignatureOK {
				t.Fatal("the manifest signature accepted a file name added after signing")
			}
		},
	)

	t.Run(
		"verifier rejects removed file name",
		func(t *testing.T) {
			dir := t.TempDir()
			orgID := uuid.Must(uuid.NewV7())
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}

			manifest, err := Export(
				context.Background(),
				exportTestRows(t, orgID, 20, nil),
				privateKey,
				"ed25519:test",
				exportTestFilter(orgID),
				dir,
			)
			if err != nil {
				t.Fatalf("export: %v", err)
			}

			// Preserve identical content under the legacy fallback name to
			// isolate signature failure after removing events_file.
			copyBundleRows(t, dir, dir, manifest.EventsFile)
			rows, err := os.ReadFile(filepath.Join(dir, manifest.EventsFile))
			if err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(
				filepath.Join(dir, legacyExportEventsFile),
				rows,
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			rewriteManifestField(t, dir, "events_file", nil)

			report, err := VerifyBundle(dir, publicKey)
			if err != nil {
				t.Fatalf("VerifyBundle: %v", err)
			}

			if !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
				t.Fatalf(
					"digest=%v hashes=%d/%d: digest and row hashes must pass before checking the signature",
					report.FileSHA256OK,
					report.HashMatches,
					report.RowsScanned,
				)
			}

			if report.SignatureOK {
				t.Fatal("the manifest signature accepted removal of its rows file name")
			}
		},
	)
}
