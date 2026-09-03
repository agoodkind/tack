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

// exportScaleRows is the size the memory assertions run at. The production org
// that motivated this holds 422,580 rows; the assertions below are about the
// shape of the footprint rather than a row count, so they extrapolate.
const exportScaleRows = 50000

// exportPeakHeapBudgetBytes is what a streaming export is allowed to hold, at
// any row count. It covers the write buffer, the encoder's scratch, one row,
// and whatever garbage the collector has not reclaimed yet. It sits far below
// what holding the rows costs: 50,000 of these rows run to tens of megabytes,
// so an export that collected them before writing fails here.
const exportPeakHeapBudgetBytes = 16 << 20

// TestExportPeakMemoryIsFlatInRowCount is the scale assertion. It runs the same
// export at two sizes four times apart and requires the footprint not to follow
// the row count. An export that collects its rows before writing grows with the
// ledger by construction, which is what made a production-sized org
// unexportable inside the 512 MiB ops sidecar; an export that writes and
// releases each row costs the same at any size.
//
// The peak is sampled while the export runs rather than read after it returns,
// so a collected slice cannot be reclaimed before the measurement lands.
func TestExportPeakMemoryIsFlatInRowCount(t *testing.T) {
	small := measureExportPeakHeap(t, exportScaleRows/4)
	large := measureExportPeakHeap(t, exportScaleRows)

	if large > exportPeakHeapBudgetBytes {
		t.Fatalf("peak heap %d bytes over %d rows exceeds the %d-byte budget; the export is retaining rows",
			large, exportScaleRows, exportPeakHeapBudgetBytes)
	}
	// Four times the rows for at most twice the memory. A footprint that tracks
	// the row count lands near four times and fails here, and the ratio keeps
	// this honest without depending on one tuned number.
	if large > small*2 {
		t.Fatalf("peak heap grew from %d bytes at %d rows to %d bytes at %d rows; four times the rows must not cost four times the memory",
			small, exportScaleRows/4, large, exportScaleRows)
	}
	// Extrapolate to the org that motivated this. The footprint is flat, so the
	// projection is the measurement, and it has to clear the tightest container
	// the export runs in.
	if large > opsSidecarMemLimitBytes {
		t.Fatalf("peak heap %d bytes exceeds the %d-byte ops sidecar limit",
			large, int(opsSidecarMemLimitBytes))
	}
	t.Logf("peak heap: %d bytes at %d rows, %d bytes at %d rows",
		small, exportScaleRows/4, large, exportScaleRows)
}

// measureExportPeakHeap exports rowCount rows and returns the highest live-heap
// reading taken while the export was running.
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
