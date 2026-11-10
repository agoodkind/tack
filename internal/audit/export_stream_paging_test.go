// These tests check that a zero-limit export writes all matching rows even when
// their count exceeds ledger page sizes.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportPageSpanRows exceeds both ledger page limits. [DefaultQueryPageLimit]
// permits 100 rows and [MaxQueryPageLimit] permits 1000 rows. Accidentally
// applying either page size must truncate this fixture.
const exportPageSpanRows = 2500

// TestExportWritesEveryRowPastEveryPageSize checks the regression from using a
// paged read for whole-range export. That earlier query interpreted zero as one
// page. The signed manifest and digest then certified only the written page
// without exposing omitted ledger rows. The fixture honors limits and must fail
// an export that asks for a page.
//
// These assertions cannot distinguish streaming from collecting all rows before
// writing: both produce the same bundle bytes.
// [TestExportPeakMemoryIsFlatInRowCount] separately samples heap use during
// export to check row retention.
func TestExportWritesEveryRowPastEveryPageSize(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	source := &ledgerRowStream{
		t:       t,
		orgID:   orgID,
		rows:    exportPageSpanRows,
		shards:  8,
		base:    time.Now().UTC().Add(-time.Duration(exportPageSpanRows) * time.Second),
		observe: nil,
	}

	manifest, err := Export(
		context.Background(),
		source,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if manifest.RowCount != exportPageSpanRows {
		t.Fatalf(
			"manifest row count = %d, want %d; a zero limit must export every row, not a page",
			manifest.RowCount,
			exportPageSpanRows,
		)
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

	// The signature covers the manifest's row count and digest. These checks
	// establish that the file matches those signed values; the expected fixture
	// count separately detects a read that stopped at one page.
	if !report.FileSHA256OK || !report.SignatureOK {
		t.Fatalf(
			"digest ok = %v, signature ok = %v, want both true",
			report.FileSHA256OK,
			report.SignatureOK,
		)
	}

	if verdict := report.Err(); verdict != nil {
		t.Fatalf("a complete export failed verification: %v", verdict)
	}
}
