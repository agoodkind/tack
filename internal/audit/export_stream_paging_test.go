// export_stream_paging_test.go checks that zero-limit exports exceed ledger page limits.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportPageSpanRows exceeds both DefaultQueryPageLimit and MaxQueryPageLimit.
const exportPageSpanRows = 2500

// TestExportWritesEveryRowPastEveryPageSize checks complete zero-limit exports
// using a source that honors requested limits. Memory accumulation requires
// the separate TestExportPeakMemoryIsFlatInRowCount measurements.
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

	// Require the signed count and digest to agree with the actual file.
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
