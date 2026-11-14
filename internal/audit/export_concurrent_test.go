// export_concurrent_test.go checks deterministic interleavings of exports in
// one directory without overwriting another export's rows or reclaiming active files.

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

// TestInterleavedExportsPublishDistinctRows completes a second export halfway
// through the first. The first publishes last and must verify against its own
// distinct rows file rather than the second export's content.
func TestInterleavedExportsPublishDistinctRows(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const firstRows, secondRows = 400, 120
	var secondManifest *ExportManifest
	var secondErr error
	first := exportTestRows(
		t,
		orgID,
		firstRows,
		func(index int) {
			if index != firstRows/2 {
				return
			}

			secondManifest, secondErr = Export(
				context.Background(),
				exportTestRows(t, orgID, secondRows, nil),
				privateKey,
				"ed25519:test",
				exportTestFilter(orgID),
				dir,
			)
		},
	)

	firstManifest, err := Export(
		context.Background(),
		first,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if secondErr != nil {
		t.Fatalf("the export that ran while another was mid-stream failed: %v", secondErr)
	}

	if err != nil {
		t.Fatalf("the export that was mid-stream when another ran failed: %v", err)
	}

	if secondManifest.RowCount != secondRows {
		t.Fatalf("second export counted %d rows, want %d", secondManifest.RowCount, secondRows)
	}

	if firstManifest.RowCount != firstRows {
		t.Fatalf("first export counted %d rows, want %d", firstManifest.RowCount, firstRows)
	}

	if firstManifest.EventsFile == secondManifest.EventsFile {
		t.Fatalf("both exports named the same rows file %q", firstManifest.EventsFile)
	}

	// Verify the first export's manifest and rows after its later publication.
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify the surviving bundle: %v", err)
	}

	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle two exports left behind does not verify: %v", verdict)
	}

	if report.RowsScanned != firstRows {
		t.Fatalf(
			"the published bundle contains %d rows, want the %d the last publish wrote",
			report.RowsScanned,
			firstRows,
		)
	}

	assertDirectoryHoldsOnlyItsBundle(t, dir)
}

// TestExportReclamationPreservesActiveRows completes an export while another
// still writes unreferenced rows. Reclamation must preserve the active files
// and their eventual verifiable bundle.
func TestExportReclamationPreservesActiveRows(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const streamingRows, finishingRows = 400, 60
	var missing []string
	streaming := exportTestRows(
		t,
		orgID,
		streamingRows,
		func(index int) {
			if index != streamingRows/2 {
				return
			}

			inFlight := bundleRowsFileNames(t, dir)
			if _, err := Export(
				context.Background(),
				exportTestRows(t, orgID, finishingRows, nil),
				privateKey,
				"ed25519:test",
				exportTestFilter(orgID),
				dir,
			); err != nil {
				t.Errorf("the export that finished mid-stream failed: %v", err)
				return
			}

			for _, name := range inFlight {
				if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
					missing = append(missing, name)
				}
			}
		},
	)

	manifest, err := Export(
		context.Background(),
		streaming,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err != nil {
		t.Fatalf("the export that was mid-stream when another finished failed: %v", err)
	}

	if len(missing) > 0 {
		t.Fatalf(
			"a finishing export reclaimed %v, which the export still writing them was about to publish",
			missing,
		)
	}

	if manifest.RowCount != streamingRows {
		t.Fatalf("row count = %d, want %d", manifest.RowCount, streamingRows)
	}

	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle published after a concurrent reclaim does not verify: %v", verdict)
	}

	assertDirectoryHoldsOnlyItsBundle(t, dir)
}
