// These tests run one export to completion while another still writes rows in
// the same directory. They check independent bundle publication and
// preservation of in-progress rows that filenames alone cannot distinguish from
// abandoned rows.

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

// TestASecondExportIntoTheSameDirectoryPublishesItsOwnRows checks that exports
// sharing a directory write separate rows files. Earlier exports shared one
// filename. A second create truncated the first export's open file, and the
// first continued writing bytes the second had already published. The directory
// then paired one run's rows with another run's signed manifest.
//
// The test deterministically completes the second export halfway through the
// first export's rows. This reproduces the harmful concurrent order without
// depending on a race that occurs only sometimes.
func TestASecondExportIntoTheSameDirectoryPublishesItsOwnRows(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const firstRows, secondRows = 400, 120
	var secondManifest *ExportManifest
	var secondErr error
	first := exportTestRows(t, orgID, firstRows, func(index int) {
		if index != firstRows/2 {
			return
		}
		secondManifest, secondErr = Export(context.Background(), exportTestRows(t, orgID, secondRows, nil),
			privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	})

	firstManifest, err := Export(
		context.Background(), first, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
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

	// The first export publishes last. Its manifest must match its own rows; the
	// earlier shared filename allowed another run's rows in that signed bundle.
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify the surviving bundle: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle two exports left behind does not verify: %v", verdict)
	}
	if report.RowsScanned != firstRows {
		t.Fatalf("the published bundle holds %d rows, want the %d the last publish wrote",
			report.RowsScanned, firstRows)
	}
	assertDirectoryHoldsOnlyItsBundle(t, dir)
}

// TestTheReclaimSparesTheRowsOfAnExportStillWriting checks that reclamation
// preserves an active export's rows. A manifest may omit both abandoned rows
// and rows that an export still writes. Deleting the active file lets its
// writer finish an unlinked file and report success with a manifest selecting a
// missing file.
//
// One export completes while another still writes rows. At that point, a
// filename-only check cannot distinguish those rows from abandoned files.
func TestTheReclaimSparesTheRowsOfAnExportStillWriting(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const streamingRows, finishingRows = 400, 60
	var missing []string
	streaming := exportTestRows(t, orgID, streamingRows, func(index int) {
		if index != streamingRows/2 {
			return
		}
		inFlight := bundleRowsFileNames(t, dir)
		if _, err := Export(context.Background(), exportTestRows(t, orgID, finishingRows, nil),
			privateKey, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
			t.Errorf("the export that finished mid-stream failed: %v", err)
			return
		}
		for _, name := range inFlight {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
				missing = append(missing, name)
			}
		}
	})

	manifest, err := Export(
		context.Background(), streaming, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("the export that was mid-stream when another finished failed: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("a finishing export reclaimed %v, which the export still writing them was about to publish",
			missing)
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
