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

// exportScaleRows sets the larger input for the memory assertions. The
// production organization that motivated them contained 422,580 rows. The
// assertions compare memory growth at smaller sizes to estimate behavior at
// that production size.
const exportScaleRows = 50000

// exportPeakHeapBudgetBytes limits peak heap use during export at either test
// size. The budget includes the write buffer, encoder scratch space, one row,
// and garbage that collection has not reclaimed. Retaining 50,000 fixture rows
// requires tens of megabytes and must exceed this budget.
const exportPeakHeapBudgetBytes = 16 << 20

// TestExportPeakMemoryIsFlatInRowCount compares peak heap use at input sizes
// that differ by a factor of four. Collecting all rows makes memory grow with
// the ledger. That earlier design prevented a production-sized export inside
// the 512 MiB ops sidecar. Processing and releasing each row avoids retaining
// that full slice.
//
// The test samples heap use during export. Sampling only after return could
// miss a collected slice that garbage collection had already reclaimed.
func TestExportPeakMemoryIsFlatInRowCount(t *testing.T) {
	small := measureExportPeakHeap(t, exportScaleRows/4)
	large := measureExportPeakHeap(t, exportScaleRows)

	if large > exportPeakHeapBudgetBytes {
		t.Fatalf("peak heap %d bytes over %d rows exceeds the %d-byte budget; the export is retaining rows",
			large, exportScaleRows, exportPeakHeapBudgetBytes)
	}
	// Four times as many rows may use at most twice the measured heap. Memory
	// proportional to row count would approach a factor of four and fail this ratio
	// check independently of the absolute budget.
	if large > small*2 {
		t.Fatalf("peak heap grew from %d bytes at %d rows to %d bytes at %d rows; four times the rows must not cost four times the memory",
			small, exportScaleRows/4, large, exportScaleRows)
	}
	// Compare the measured heap with the ops sidecar limit. Extrapolation to the
	// motivating production ledger assumes that memory remains flat beyond these
	// test sizes.
	if large > opsSidecarMemLimitBytes {
		t.Fatalf("peak heap %d bytes exceeds the %d-byte ops sidecar limit",
			large, int(opsSidecarMemLimitBytes))
	}
	t.Logf("peak heap: %d bytes at %d rows, %d bytes at %d rows",
		small, exportScaleRows/4, large, exportScaleRows)
}

// measureExportPeakHeap exports rowCount rows and returns the highest sampled
// live-heap reading during that export.
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
		t: t, orgID: orgID, rows: rowCount, shards: 8,
		base: time.Now().UTC().Add(-time.Duration(rowCount) * time.Second),
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
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export %d rows: %v", rowCount, err)
	}
	if manifest.RowCount != rowCount {
		t.Fatalf("manifest row count = %d, want %d", manifest.RowCount, rowCount)
	}
	return peakHeap
}
