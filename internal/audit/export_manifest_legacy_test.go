// export_manifest_legacy_test.go covers bundles written before the manifest
// carried an events file name: their format fixes the name, and they still
// verify.

package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestABundleWrittenBeforeTheNameExistedStillVerifies pins what operators
// holding older bundles get. Those manifests name no events file, and their
// format fixed the name instead, so the name is resolved from the format rather
// than from anything unsigned. An archive that could no longer be verified
// would be a compliance record that had quietly expired.
func TestABundleWrittenBeforeTheNameExistedStillVerifies(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 4)
	pub := writeSignedExportTestBundle(t, dir, rows)
	if _, err := os.Stat(filepath.Join(dir, legacyExportEventsFile)); err != nil {
		t.Fatalf("the fixture must be a bundle in the older layout: %v", err)
	}
	manifest, err := readExportManifest(dir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.EventsFile != "" {
		t.Fatalf("events_file = %q, want the older layout's empty name", manifest.EventsFile)
	}

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify an older bundle: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("an older bundle no longer verifies: %v", verdict)
	}
	if report.RowsScanned != len(rows) {
		t.Fatalf("rows scanned = %d, want %d", report.RowsScanned, len(rows))
	}
}
