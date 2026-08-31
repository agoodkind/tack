// export_publish_test.go covers how an export becomes the bundle in a
// directory: what a publish that cannot finish leaves behind, and what two
// exports writing into one directory do to each other.

package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// assertNoStagedFiles fails when an export left a staged file in the bundle
// directory. A leftover partial grows the directory every time an export fails,
// and the per-export name means no later run reclaims it.
func assertNoStagedFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), stagedSuffix) {
			t.Fatalf("the failed export left %s behind", entry.Name())
		}
	}
}

// TestFailedPublishLeavesThePublishedManifestInPlace pins the window between
// the two renames that publish a bundle. Publishing used to clear the published
// manifest before either rename ran, which meant a rename that failed, or a
// crash before the second one, destroyed the signed manifest of a bundle that
// was whole a moment earlier and left nothing that could be verified.
//
// The failure is induced by making the published events path something a rename
// cannot replace, which is the only way to fail that rename through the export's
// own API. What it stands in for is any failure of it, the crash included.
func TestFailedPublishLeavesThePublishedManifestInPlace(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const publishedRows = 40
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: publishedRows, shards: 4,
		base: time.Now().UTC().Add(-publishedRows * time.Second), observe: nil,
	}
	if _, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
		t.Fatalf("first export: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read published manifest: %v", err)
	}

	// A directory cannot be replaced by renaming a file over it, so the rows
	// rename fails and the publish stops there.
	rowsPath := filepath.Join(dir, "events.jsonl")
	if err := os.Remove(rowsPath); err != nil {
		t.Fatalf("clear published rows: %v", err)
	}
	if err := os.Mkdir(rowsPath, 0o700); err != nil {
		t.Fatalf("block the rows path: %v", err)
	}

	retry := &ledgerRowStream{
		t: t, orgID: orgID, rows: publishedRows, shards: 4,
		base: time.Now().UTC().Add(-publishedRows * time.Second), observe: nil,
	}
	if _, err := Export(
		context.Background(), retry, privateKey, "ed25519:test", exportTestFilter(orgID), dir); err == nil {
		t.Fatal("the export reported success on a publish that could not rename its rows into place")
	}

	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("the failed publish destroyed the published manifest: %v", err)
	}
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatalf("the failed publish rewrote the published manifest: %d bytes before, %d after",
			len(manifestBefore), len(manifestAfter))
	}
	assertNoStagedFiles(t, dir)
}

// TestASecondExportIntoTheSameDirectoryPublishesItsOwnRows pins that two
// exports sharing a directory do not write each other's files. The staged names
// used to be fixed, so the second export's create truncated the first one's
// staged rows while the first still held the descriptor: the first went on
// writing into a file the second had already published, and the directory ended
// up holding one run's rows under the other run's signed manifest. That is a
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
	first := &ledgerRowStream{
		t: t, orgID: orgID, rows: firstRows, shards: 4,
		base: time.Now().UTC().Add(-firstRows * time.Second),
		observe: func(index int) {
			if index != firstRows/2 {
				return
			}
			second := &ledgerRowStream{
				t: t, orgID: orgID, rows: secondRows, shards: 4,
				base: time.Now().UTC().Add(-secondRows * time.Second), observe: nil,
			}
			secondManifest, secondErr = Export(context.Background(), second, privateKey,
				"ed25519:test", exportTestFilter(orgID), dir)
		},
	}

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

	// The first export published last, so its pair is what the directory holds.
	// What matters is that the pair is one run's: a manifest signed for rows the
	// file does not hold is exactly what shared staged names produced.
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
	assertNoStagedFiles(t, dir)
}
