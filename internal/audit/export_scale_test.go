package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportScaleRows sets the larger sample size for export memory assertions.
const exportScaleRows = 50000

// exportPeakHeapBudgetBytes limits sampled heap for the write buffer, encoder,
// current row, and uncollected garbage. Collecting the entire fixture exceeds it.
const exportPeakHeapBudgetBytes = 16 << 20

// TestExportPeakMemoryIsFlatInRowCount compares sampled heap at row counts four
// times apart. Sampling during export prevents post-export garbage collection
// from hiding row accumulation. Both samples must fit the ops container budget.
func TestExportPeakMemoryIsFlatInRowCount(t *testing.T) {
	small := measureExportPeakHeap(t, exportScaleRows/4)
	large := measureExportPeakHeap(t, exportScaleRows)

	if large > exportPeakHeapBudgetBytes {
		t.Fatalf(
			"peak heap %d bytes over %d rows exceeds the %d-byte budget; the export is retaining rows",
			large,
			exportScaleRows,
			exportPeakHeapBudgetBytes,
		)
	}

	// Require at most twice the sampled heap for four times the rows.
	if large > small*2 {
		t.Fatalf(
			"peak heap grew from %d bytes at %d rows to %d bytes at %d rows; four times the rows must not cost four times the memory",
			small,
			exportScaleRows/4,
			large,
			exportScaleRows,
		)
	}

	// Compare the larger sample with the ops sidecar's memory limit.
	if large > opsSidecarMemLimitBytes {
		t.Fatalf(
			"peak heap %d bytes exceeds the %d-byte ops sidecar limit",
			large,
			int(opsSidecarMemLimitBytes),
		)
	}

	t.Logf(
		"peak heap: %d bytes at %d rows, %d bytes at %d rows",
		small,
		exportScaleRows/4,
		large,
		exportScaleRows,
	)
}

// measureExportPeakHeap returns the highest live-heap sample during export.
func measureExportPeakHeap(t *testing.T, rowCount int) uint64 {
	t.Helper()
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const sampleEvery = 500
	var peakHeap uint64
	var stats runtime.MemStats
	source := &ledgerRowStream{
		t:      t,
		orgID:  orgID,
		rows:   rowCount,
		shards: 8,
		base:   time.Now().UTC().Add(-time.Duration(rowCount) * time.Second),
		observe: func(index int) {
			if index%sampleEvery != 0 {
				return
			}

			runtime.ReadMemStats(&stats)
			if stats.HeapAlloc > peakHeap {
				peakHeap = stats.HeapAlloc
			}
		},
	}

	runtime.GC()
	manifest, err := Export(
		context.Background(),
		source,
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err != nil {
		t.Fatalf("export %d rows: %v", rowCount, err)
	}

	if manifest.RowCount != rowCount {
		t.Fatalf("manifest row count = %d, want %d", manifest.RowCount, rowCount)
	}

	return peakHeap
}
