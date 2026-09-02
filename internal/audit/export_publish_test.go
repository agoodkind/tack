// export_publish_test.go covers how an export becomes the bundle in a
// directory: what a publish that cannot finish leaves behind, what two exports
// writing into one directory do to each other, and which files a later export
// is allowed to free.

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
	"time"

	"github.com/google/uuid"
)

// bundleRowsFileNames returns the names of the per-export rows files sitting in
// a bundle directory, whatever export wrote them.
func bundleRowsFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, exportEventsPrefix) && strings.HasSuffix(name, exportEventsSuffix) {
			names = append(names, name)
		}
	}
	return names
}

// publishedBundle reads the manifest a bundle directory publishes and the bytes
// of the rows file it names, which is the only pair that is the bundle.
func publishedBundle(t *testing.T, dir string) (ExportManifest, []byte) {
	t.Helper()
	manifest, err := readExportManifest(dir)
	if err != nil {
		t.Fatalf("read published manifest: %v", err)
	}
	name, err := bundleEventsFileName(manifest)
	if err != nil {
		t.Fatalf("resolve the manifest's events file: %v", err)
	}
	rows, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read the rows the manifest names: %v", err)
	}
	return manifest, rows
}

// assertDirectoryHoldsOnlyItsBundle fails when a bundle directory carries a
// file beyond the bundle itself: a staged file a failed export left, or the
// rows of an export that was superseded or died. The allowed names are listed
// here rather than read from the production predicate, so a change that
// widened what counts as reclaimable could not also widen this check.
func assertDirectoryHoldsOnlyItsBundle(t *testing.T, dir string) {
	t.Helper()
	allowed := map[string]bool{exportManifestFile: true, exportActivityLockFile: true}
	if manifest, err := readExportManifest(dir); err == nil {
		name, nameErr := bundleEventsFileName(manifest)
		if nameErr != nil {
			t.Fatalf("resolve the manifest's events file: %v", nameErr)
		}
		allowed[name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			t.Fatalf("the bundle directory still holds %s, which is not part of the published bundle",
				entry.Name())
		}
	}
}

// exportTestRows streams a correctly chained ledger of the requested size.
func exportTestRows(t *testing.T, orgID uuid.UUID, rows int, observe func(int)) *ledgerRowStream {
	t.Helper()
	return &ledgerRowStream{
		t: t, orgID: orgID, rows: rows, shards: 4,
		base: time.Now().UTC().Add(-time.Duration(rows) * time.Second), observe: observe,
	}
}

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

// copyBundleRows places one export's rows file into another directory under the
// name its manifest carries.
func copyBundleRows(t *testing.T, fromDir, toDir, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fromDir, name))
	if err != nil {
		t.Fatalf("read rows %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(toDir, name), body, 0o600); err != nil {
		t.Fatalf("place rows %s: %v", name, err)
	}
}

// TestAnExportReclaimsTheRowsNoManifestNames pins the reclaim rule. A process
// killed mid-export leaves a rows file behind, and naming those files per
// export means no later run overwrites one, so without a reclaim they
// accumulate a ledger-sized file per interruption.
//
// The rule is exact rather than a guess about age: a bundle is a manifest and
// the rows it names, so every other file this exporter wrote is free. The
// superseded rows of the directory's previous bundle are covered by the same
// rule and are freed by the same pass.
func TestAnExportReclaimsTheRowsNoManifestNames(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Everything an interrupted export can leave: a rows file named for the
	// export that abandoned it, the staged rows name earlier revisions wrote,
	// and a staged manifest.
	abandoned := []string{
		exportEventsFileName(uuid.Must(uuid.NewV7())),
		legacyExportEventsFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
		exportManifestFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
	}
	for _, name := range abandoned {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("half a ledger\n"), 0o600); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	firstManifest, err := Export(context.Background(), exportTestRows(t, orgID, 40, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, name := range abandoned {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Fatalf("the export left the abandoned %s in place", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, firstManifest.EventsFile)); statErr != nil {
		t.Fatalf("the export reclaimed the rows its own manifest names: %v", statErr)
	}

	// A second export supersedes the first, so the first's rows stop being named
	// and the same rule frees them.
	secondManifest, err := Export(context.Background(), exportTestRows(t, orgID, 25, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, firstManifest.EventsFile)); statErr == nil {
		t.Fatal("the superseded rows of the previous bundle were kept")
	}
	if _, statErr := os.Stat(filepath.Join(dir, secondManifest.EventsFile)); statErr != nil {
		t.Fatalf("the published rows were reclaimed: %v", statErr)
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle left after two reclaims does not verify: %v", verdict)
	}
	assertDirectoryHoldsOnlyItsBundle(t, dir)
}

// TestTheReclaimSparesFilesThisExporterDidNotWrite pins the limit of the
// reclaim rule. A bundle directory is somewhere an operator also works, and the
// reclaim unlinks without asking, so what counts as this exporter's file has to
// be the names it actually produces rather than the shape those names have.
//
// Every name in the operator group used to be reclaimed: the rule was a prefix
// and a suffix with anything at all between them, so a file an auditor left
// beside the bundle was deleted by the next export purely for being called
// something of that form. Each name here is one an export cannot have written,
// because this exporter puts the id of the export into every name it writes and
// renders it one way.
//
// The exporter group is the control. Without it a predicate that simply stopped
// reclaiming anything would pass this test while breaking the reclaim.
func TestTheReclaimSparesFilesThisExporterDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	realExportID := uuid.Must(uuid.NewV7()).String()
	operatorFiles := []string{
		"events-before-the-incident.jsonl",
		"events-2026-05-01.jsonl",
		"events-.jsonl",
		"events-" + strings.ToUpper(realExportID) + ".jsonl",
		"events-{" + realExportID + "}.jsonl",
		"events-urn:uuid:" + realExportID + ".jsonl",
		exportManifestFile + ".auditor-notes" + stagedSuffix,
		legacyExportEventsFile + ".backup" + stagedSuffix,
	}
	exporterFiles := []string{
		exportEventsFileName(uuid.Must(uuid.NewV7())),
		legacyExportEventsFile,
		legacyExportEventsFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
		exportManifestFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
	}
	for _, name := range append(append([]string{}, operatorFiles...), exporterFiles...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("planted\n"), 0o600); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	manifest, err := Export(context.Background(), exportTestRows(t, orgID, 30, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	for _, name := range operatorFiles {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Errorf("the reclaim removed %s, which no export wrote: %v", name, statErr)
		}
	}
	for _, name := range exporterFiles {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Errorf("the reclaim kept %s, which an export wrote and no manifest names", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, manifest.EventsFile)); statErr != nil {
		t.Fatalf("the reclaim removed the rows this bundle's manifest names: %v", statErr)
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the published bundle does not verify: %v", verdict)
	}
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
