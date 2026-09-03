// export_publish_test.go covers how an export becomes the bundle in a
// directory: what a publish that cannot finish leaves behind, and that either
// order two publishes can land in leaves a bundle that verifies.

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

// TestAFailedPublishLeavesThePublishedBundleIntact pins the one window a
// publish still has. Publishing used to clear the published manifest before
// either of its two renames ran, so a rename that failed, or a crash before the
// second one, destroyed the signed manifest of a bundle that was whole a moment
// earlier and left nothing that could be verified.
//
// The failure is induced where the export writes the manifest it is about to
// publish: the running export's rows file names its export id, so the test can
// derive the staged manifest path and block it with a directory, which no write
// can replace. What that stands in for is any failure of the publish, the crash
// included.
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

// TestEitherPublicationOrderLeavesAVerifyingBundle is the finding this file's
// naming exists to remove. Two exports into one directory used to publish two
// files each, so the renames could interleave and leave one run's rows beside
// the other run's manifest; the digest check caught that pair, but a check that
// catches a wrong pair is weaker than a directory in which the wrong pair
// cannot exist.
//
// Both rows files sit in the directory at once, under the names their own
// manifests carry, and each publication order is applied through the production
// publish. Whichever manifest lands second is the bundle, and both outcomes
// verify, because a manifest can only name the rows it was signed for.
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
