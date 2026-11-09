// These tests check that a failed publication preserves the existing bundle and
// that either publication order of two exports produces a bundle that verifies.

package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAFailedPublishLeavesThePublishedBundleIntact checks publication failure
// after an existing bundle succeeds. Earlier publication code deleted the
// published manifest before either of two renames. A failed rename or a crash
// before the second rename then destroyed the signed manifest of a complete
// bundle.
//
// The test derives the staged manifest path from the running export's rows
// filename and creates a directory at that path. The manifest write cannot
// replace that directory. This checks preservation after a publication write
// failure; it does not simulate a process crash.
func TestAFailedPublishLeavesThePublishedBundleIntact(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const publishedRows = 40
	if _, err := Export(context.Background(), exportTestRows(t, orgID, publishedRows, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
		t.Fatalf("first export: %v", err)
	}
	manifestBefore, rowsBefore := publishedBundle(t, dir)
	published := bundleRowsFileNames(t, dir)

	blockStagedManifest := func(int) {
		for _, name := range bundleRowsFileNames(t, dir) {
			if slices.Contains(published, name) {
				continue
			}
			exportID := strings.TrimSuffix(strings.TrimPrefix(name, exportEventsPrefix), exportEventsSuffix)
			staged := filepath.Join(dir, exportManifestFile+"."+exportID+stagedSuffix)
			if mkdirErr := os.Mkdir(staged, 0o700); mkdirErr != nil && !os.IsExist(mkdirErr) {
				t.Fatalf("block the staged manifest: %v", mkdirErr)
			}
		}
	}
	if _, err := Export(context.Background(), exportTestRows(t, orgID, publishedRows, blockStagedManifest),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir); err == nil {
		t.Fatal("the export reported success on a publish that could not write its manifest")
	}

	manifestAfter, rowsAfter := publishedBundle(t, dir)
	if manifestAfter.Signature != manifestBefore.Signature {
		t.Fatal("the failed publish replaced the published manifest")
	}
	if !bytes.Equal(rowsBefore, rowsAfter) {
		t.Fatalf("the failed publish rewrote the published rows: %d bytes before, %d after",
			len(rowsBefore), len(rowsAfter))
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("the bundle that survived the failed publish no longer verifies: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("surviving bundle failed verification: %v", verdict)
	}
	assertDirectoryHoldsOnlyItsBundle(t, dir)
}

// TestEitherPublicationOrderLeavesAVerifyingBundle checks both orders of two
// manifest publications. Earlier exports each published two files, and
// interleaved renames could pair one export's rows with the other's manifest.
// The digest check detected that invalid pair, but publication did not prevent
// it.
//
// The test places both rows files in one directory under their export-specific
// names and uses the production publication function in each order. The last
// manifest selects its own signed rows, and both resulting bundles must verify.
func TestEitherPublicationOrderLeavesAVerifyingBundle(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const firstRows, secondRows = 40, 90
	firstDir, secondDir := t.TempDir(), t.TempDir()
	firstManifest, err := Export(context.Background(), exportTestRows(t, orgID, firstRows, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), firstDir)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	secondManifest, err := Export(context.Background(), exportTestRows(t, orgID, secondRows, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), secondDir)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	orders := []struct {
		name  string
		order []*ExportManifest
		rows  int
	}{
		{name: "first publishes last", order: []*ExportManifest{secondManifest, firstManifest}, rows: firstRows},
		{name: "second publishes last", order: []*ExportManifest{firstManifest, secondManifest}, rows: secondRows},
	}
	for _, testCase := range orders {
		t.Run(testCase.name, func(t *testing.T) {
			shared := t.TempDir()
			copyBundleRows(t, firstDir, shared, firstManifest.EventsFile)
			copyBundleRows(t, secondDir, shared, secondManifest.EventsFile)
			for _, manifest := range testCase.order {
				if err := publishExportManifest(context.Background(), shared, manifest.ExportID, manifest); err != nil {
					t.Fatalf("publish %s: %v", manifest.ExportID, err)
				}
			}

			report, err := VerifyBundle(shared, publicKey)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if verdict := report.Err(); verdict != nil {
				t.Fatalf("the bundle this publication order left does not verify: %v", verdict)
			}
			if report.RowsScanned != testCase.rows {
				t.Fatalf("rows scanned = %d, want the %d rows the last publish signed for",
					report.RowsScanned, testCase.rows)
			}
		})
	}
}
