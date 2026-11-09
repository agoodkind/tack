// These tests verify older bundles that omit the events filename. Their
// original format fixes that filename.

package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestABundleWrittenBeforeTheNameExistedStillVerifies checks verification of
// older compliance archives. Those manifests omit EventsFile, and the verifier
// uses their format's fixed filename rather than unsigned input. Continued
// verification preserves the usefulness of those archived compliance records.
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
