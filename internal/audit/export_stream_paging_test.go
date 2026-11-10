// export_stream_paging_test.go covers paging and limit semantics: a zero-limit
// export reads every row past every page size the ledger reads by.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportPageSpanRows is larger than every page size the ledger reads by, so an
// export that silently inherited one would come back short. DefaultQueryPageLimit
// is 100 and MaxQueryPageLimit is 1000.
const exportPageSpanRows = 2500

// TestExportWritesEveryRowPastEveryPageSize covers the page-size half of the
// defect this file exists for. The export used to read its rows through the
// page-sized query, which defaults a zero limit to one page, so a whole-range
// export came back holding a page and the bundle still read as complete: the
// manifest counts what was written, the digest covers what was written, and
// nothing in the bundle records what was left behind. The fixture honours the
// filter's limit, so an export that asked for a page gets a page and fails here.
//
// The other half of that defect, holding every row before writing any of them,
// is invisible to every assertion below: an export that collected its rows first
// writes the same bytes and signs the same manifest. It is a memory property, and
// TestExportPeakMemoryIsFlatInRowCount is what pins it, by sampling the heap
// while the export is still running.
func TestExportWritesEveryRowPastEveryPageSize(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: exportPageSpanRows, shards: 8,
		base: time.Now().UTC().Add(-time.Duration(exportPageSpanRows) * time.Second), observe: nil,
	}

	manifest, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != exportPageSpanRows {
		t.Fatalf("manifest row count = %d, want %d; a zero limit must export every row, not a page",
			manifest.RowCount, exportPageSpanRows)
	}

	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.RowsScanned != exportPageSpanRows {
		t.Fatalf("rows scanned = %d, want %d", report.RowsScanned, exportPageSpanRows)
	}
	if report.HashMatches != exportPageSpanRows {
		t.Fatalf("hash matches = %d, want %d", report.HashMatches, exportPageSpanRows)
	}
	if report.ChainGapCount != 0 {
		t.Fatalf("sequence gaps = %d in a whole-ledger export, want 0", report.ChainGapCount)
	}
	// The manifest is signed over the row count and the digest, so a bundle that
	// verifies is one whose count and digest both describe the rows the file
	// actually holds, rather than a manifest signed for a read that came back
	// short.
	if !report.FileSHA256OK || !report.SignatureOK {
		t.Fatalf("digest ok = %v, signature ok = %v, want both true",
			report.FileSHA256OK, report.SignatureOK)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("a complete export failed verification: %v", verdict)
	}
}
