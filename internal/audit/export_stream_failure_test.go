// These tests check failed ledger reads in empty export directories and
// directories that already contain a published bundle.

package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestExportStopsWhenTheLedgerReadFails checks that a failed read cannot
// publish a signed incomplete bundle. Publishing a manifest after an early
// stream failure would certify a truncated export.
//
// The test fails the read before its first row and after rows have filled and
// flushed the write buffer. The later failure exercises partially written
// output, as a dropped connection during a large ledger read can produce.
func TestExportStopsWhenTheLedgerReadFails(t *testing.T) {
	cases := []struct {
		name              string
		rowsBeforeFailure int
	}{
		{name: "before any row reaches the export", rowsBeforeFailure: 0},
		{name: "after rows are already written", rowsBeforeFailure: exportFailureRows},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			orgID := uuid.Must(uuid.NewV7())
			_, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}

			source := &failingRowSource{t: t, orgID: orgID, rowsBeforeFailure: testCase.rowsBeforeFailure}
			if _, err := Export(context.Background(), source, privateKey, "ed25519:test",
				exportTestFilter(orgID), dir); err == nil {
				t.Fatal("export reported success on a ledger read that failed")
			}
			if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
				t.Fatal("a failed export left a signed manifest behind")
			}
			assertDirectoryHoldsOnlyItsBundle(t, dir)
		})
	}
}

// exportFailureRows sets the number of rows supplied before the injected read
// failure. The re-export test checks that their encoded size exceeds the write
// buffer. A failure confined to buffered bytes would not exercise partially
// written file cleanup.
const exportFailureRows = 3000

// failingRowSource supplies rowsBeforeFailure rows and then returns a read
// error, like a connection that fails during a large export.
//
// A failure before the first row checks only whether an empty failed read
// avoids publication. Supplying rows first also exercises implementations that
// damage an existing bundle after writing begins.
type failingRowSource struct {
	t                 *testing.T
	orgID             uuid.UUID
	rowsBeforeFailure int
}

func (s *failingRowSource) StreamQuery(ctx context.Context, filter QueryFilter, visit RowVisitor) error {
	s.t.Helper()
	if s.rowsBeforeFailure > 0 {
		delivered := &ledgerRowStream{
			t: s.t, orgID: s.orgID, rows: s.rowsBeforeFailure, shards: 4,
			base:    time.Now().UTC().Add(-time.Duration(s.rowsBeforeFailure) * time.Second),
			observe: nil,
		}
		if err := delivered.StreamQuery(ctx, filter, visit); err != nil {
			return err
		}
	}
	return context.DeadlineExceeded
}

// TestFailedReExportLeavesThePublishedBundleIntact checks preservation of an
// existing bundle after a partial read failure. Re-export into an existing
// directory is ordinary operation. A dropped connection, full disk, or canceled
// command can interrupt that export.
//
// Earlier code wrote directly to events.jsonl and truncated the published rows.
// Another revision removed the manifest first and left the directory without
// its verification evidence throughout an export that could later fail. Both
// designs destroyed existing evidence; removing the manifest could do so
// without a reported row-content change.
//
// The injected read supplies rows before failing to exercise damage after
// writing begins. A read failure before the first row would not detect the
// direct-write defect.
func TestFailedReExportLeavesThePublishedBundleIntact(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: exportFailureRows, shards: 4,
		base: time.Now().UTC().Add(-exportFailureRows * time.Second), observe: nil,
	}

	published, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	_, rowsBefore := publishedBundle(t, dir)
	// The failing re-export supplies this same row count. Its encoded output must
	// exceed the write buffer and force file writes before failure. Otherwise the
	// preservation assertions could pass without exercising separate output files.
	if len(rowsBefore) <= exportWriteBufferBytes {
		t.Fatalf("%d rows encode to %d bytes, which the %d-byte write buffer holds entirely; the failed re-export would never reach the disk",
			exportFailureRows, len(rowsBefore), exportWriteBufferBytes)
	}

	failing := &failingRowSource{t: t, orgID: orgID, rowsBeforeFailure: exportFailureRows}
	if _, err := Export(context.Background(), failing, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir); err == nil {
		t.Fatal("the re-export reported success on a ledger read that failed")
	}

	_, rowsAfter := publishedBundle(t, dir)
	if !bytes.Equal(rowsBefore, rowsAfter) {
		t.Fatalf("the failed re-export rewrote the published rows: %d bytes before, %d after",
			len(rowsBefore), len(rowsAfter))
	}

	// Verify the surviving manifest and rows together. Preserving rows without
	// their signed manifest would still destroy the bundle's verification evidence.
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("the bundle that survived the failed re-export no longer verifies: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("surviving bundle failed verification: %v", verdict)
	}
	if report.RowsScanned != published.RowCount {
		t.Fatalf("surviving bundle holds %d rows, want the %d it was published with",
			report.RowsScanned, published.RowCount)
	}

	assertDirectoryHoldsOnlyItsBundle(t, dir)
}
