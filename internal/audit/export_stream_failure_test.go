// export_stream_failure_test.go checks cleanup after ledger read failures in
// empty directories and preservation of already-published bundles.

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

// TestExportStopsWhenTheLedgerReadFails requires an error without a published
// manifest when the read fails before rows or after writing more than a buffer.
func TestExportStopsWhenTheLedgerReadFails(t *testing.T) {
	cases := []struct {
		name              string
		rowsBeforeFailure int
	}{
		{name: "read fails before first row", rowsBeforeFailure: 0},
		{name: "read fails after buffered rows flush", rowsBeforeFailure: exportFailureRows},
	}
	for _, testCase := range cases {
		t.Run(
			testCase.name,
			func(t *testing.T) {
				dir := t.TempDir()
				orgID := uuid.Must(uuid.NewV7())
				_, privateKey, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatalf("generate key: %v", err)
				}

				source := &failingRowSource{
					t:                 t,
					orgID:             orgID,
					rowsBeforeFailure: testCase.rowsBeforeFailure,
				}
				if _, err := Export(
					context.Background(),
					source,
					privateKey,
					"ed25519:test",
					exportTestFilter(orgID),
					dir,
				); err == nil {
					t.Fatal("export reported success on a ledger read that failed")
				}

				if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
					t.Fatal("a failed export left a signed manifest behind")
				}

				assertDirectoryHoldsOnlyItsBundle(t, dir)
			},
		)
	}
}

// exportFailureRows exceeds the write buffer before the source fails.
// The re-export test checks the encoded size rather than assuming it.
const exportFailureRows = 3000

// failingRowSource visits rowsBeforeFailure rows before returning a deadline
// error, reproducing a read interrupted after partial file writes.
type failingRowSource struct {
	t                 *testing.T
	orgID             uuid.UUID
	rowsBeforeFailure int
}

func (s *failingRowSource) StreamQuery(
	ctx context.Context,
	filter QueryFilter,
	visit RowVisitor,
) error {
	s.t.Helper()
	if s.rowsBeforeFailure > 0 {
		delivered := &ledgerRowStream{
			t:       s.t,
			orgID:   s.orgID,
			rows:    s.rowsBeforeFailure,
			shards:  4,
			base:    time.Now().UTC().Add(-time.Duration(s.rowsBeforeFailure) * time.Second),
			observe: nil,
		}
		if err := delivered.StreamQuery(ctx, filter, visit); err != nil {
			return err
		}
	}

	return context.DeadlineExceeded
}

// TestFailedReExportLeavesThePublishedBundleIntact checks published row bytes
// and verification after a replacement export fails beyond its write buffer.
func TestFailedReExportLeavesThePublishedBundleIntact(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	source := &ledgerRowStream{
		t:       t,
		orgID:   orgID,
		rows:    exportFailureRows,
		shards:  4,
		base:    time.Now().UTC().Add(-exportFailureRows * time.Second),
		observe: nil,
	}

	published, err := Export(
		context.Background(),
		source,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}

	_, rowsBefore := publishedBundle(t, dir)
	// Require actual file writes before the replacement read fails. A failure
	// contained entirely in the buffer cannot test published-file preservation.
	if len(rowsBefore) <= exportWriteBufferBytes {
		t.Fatalf(
			"%d rows encode to %d bytes within the %d-byte buffer; the failed re-export would not write to disk",
			exportFailureRows,
			len(rowsBefore),
			exportWriteBufferBytes,
		)
	}

	failing := &failingRowSource{t: t, orgID: orgID, rowsBeforeFailure: exportFailureRows}
	if _, err := Export(
		context.Background(),
		failing,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	); err == nil {
		t.Fatal("the re-export reported success on a ledger read that failed")
	}

	_, rowsAfter := publishedBundle(t, dir)
	if !bytes.Equal(rowsBefore, rowsAfter) {
		t.Fatalf(
			"the failed re-export rewrote the published rows: %d bytes before, %d after",
			len(rowsBefore),
			len(rowsAfter),
		)
	}

	// Require the preserved rows and manifest to verify together.
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("the bundle that survived the failed re-export no longer verifies: %v", err)
	}

	if verdict := report.Err(); verdict != nil {
		t.Fatalf("surviving bundle failed verification: %v", verdict)
	}

	if report.RowsScanned != published.RowCount {
		t.Fatalf(
			"surviving bundle contains %d rows, want the %d it was published with",
			report.RowsScanned,
			published.RowCount,
		)
	}

	assertDirectoryHoldsOnlyItsBundle(t, dir)
}
