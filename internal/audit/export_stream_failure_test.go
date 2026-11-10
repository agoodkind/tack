// export_stream_failure_test.go covers the read failure modes: what a ledger
// read that fails partway through leaves behind, in an empty directory and in
// one that already holds a published bundle.

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

// TestExportStopsWhenTheLedgerReadFails pins that a read cut off partway
// through does not leave a signed bundle behind. A stream that ended early is
// a truncated export, and a truncated export that still writes its manifest is
// the silent short bundle this whole path exists to make impossible.
//
// Both halves of that failure space run. A read that fails before the first row
// leaves the writer with nothing, which is the easy half: no bytes were ever at
// risk. A read that fails after rows have already been encoded and flushed is
// the half where an export has something to damage, and it is the shape a
// dropped connection actually takes on a large ledger.
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

// exportFailureRows is how many rows the failing reads hand over before they
// fail. It is sized so the export has flushed its write buffer to disk before
// the failure arrives, which the re-export test below checks rather than
// assumes; a failure the writer absorbs entirely in memory never exercises what
// a partial export does to the file.
const exportFailureRows = 3000

// failingRowSource hands over rowsBeforeFailure rows and then reports the read
// failed, the shape of a connection dropped partway through a large export.
//
// The row count is the point. A source that fails before handing over anything
// tests only that a read which produced nothing publishes nothing, and every
// implementation that damages the bundle once rows start arriving passes that.
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

// TestFailedReExportLeavesThePublishedBundleIntact pins what a re-export may
// destroy, which is nothing. An operator re-exporting into a directory that
// already holds a bundle is the ordinary case, and the export can fail partway
// for reasons that have nothing to do with the bundle sitting there: a dropped
// connection, a full disk, a cancelled command. Two earlier shapes of this code
// damaged that bundle. Writing rows straight to events.jsonl truncated it at the
// first byte, leaving the old signed manifest describing rows that were gone.
// Removing the manifest first destroyed the only thing that makes the directory
// readable as a bundle, for the whole length of an export that then failed.
// Either way the operator loses evidence they still had a moment earlier, and
// the second one loses it silently.
//
// The failing re-export hands over rows before it fails, because a re-export
// that never produced a row cannot damage anything: it is the rows arriving
// that put the published bundle at risk, so a read that fails first would leave
// every implementation of this path looking correct.
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
	// The failing re-export below hands over this same number of rows, so this
	// is what establishes that its rows were flushed to disk rather than held in
	// the write buffer and discarded. A failure the buffer absorbed would never
	// have touched the filesystem, and the assertions after it would hold for a
	// reason that has nothing to do with staging.
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

	// The surviving pair still has to verify. Rows that survived beside a
	// manifest that did not would be the same defect wearing the other mask.
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
