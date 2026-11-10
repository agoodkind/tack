// export_concurrent_test.go covers what two exports writing into one directory
// do to each other. Both tests here run one export to completion while another
// is mid-stream, which is the shape in which the two are indistinguishable by
// name: one pins that each publishes its own rows, the other that the one
// finishing does not free the rows the other is still writing.

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

// TestASecondExportIntoTheSameDirectoryPublishesItsOwnRows pins that two
// exports sharing a directory do not write each other's files. The rows files
// used to share one name, so the second export's create truncated the first
// one's while the first still held the descriptor: the first went on writing
// into a file the second had already published, and the directory ended up
// holding one run's rows under the other run's signed manifest. That is a
// bundle whose manifest vouches for rows it never saw.
//
// The interleaving is deterministic rather than concurrent: the second export
// runs to completion at the point where the first is halfway through its rows,
// which is one of the orders two concurrent exports produce and the one that
// does the damage. A race would test the same thing and only sometimes.
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

	// The first export published last, so its pair is what the directory holds.
	// What matters is that the pair is one run's: a manifest signed for rows the
	// file does not hold is exactly what a shared rows name produced.
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

// TestTheReclaimSparesTheRowsOfAnExportStillWriting is what separates the
// reclaim rule from a rule that destroys bundles. "No manifest names this file"
// is true of two different files: the rows of an export that is finished, and
// the rows of an export that is still streaming into them. A reclaim that
// cannot tell them apart deletes the second, and the export writing it goes on
// filling an unlinked file and then publishes a manifest naming rows that are
// gone, reporting success as it does.
//
// The export that finishes here does so while another is mid-stream, which is
// exactly when the two are indistinguishable by name.
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
