package audit

import (
	"crypto/sha256"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

// opsSidecarMemLimitBytes is the tightest limit a real verify runs under: the
// restore drill invokes it inside the tack-ops sidecar, whose compose
// mem_limit is 512m, half the app container's. Proving the bound there proves
// it everywhere the command runs today.
const opsSidecarMemLimitBytes = 512 << 20

// verifyScaleRows is large enough to make an unbounded verifier's footprint
// obvious while keeping the test's own bundle generation quick. The
// production bundle that first failed held 420,562 rows; the assertions below
// are per row, so they extrapolate rather than depending on matching it.
const verifyScaleRows = 20000

// buildScaleBundle writes a signed bundle of n rows spread across shards, with
// each shard's rows chained so the link pass has real work to do.
func buildScaleBundle(t *testing.T, n int) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	base := time.Now().UTC().Add(-time.Duration(n) * time.Second)
	const shards = 256
	seqByShard := make(map[int16]int64, shards)
	prevByShard := make(map[int16][]byte, shards)

	rows := make([]Row, 0, n)
	for i := range n {
		shard := int16(i % shards)
		seqByShard[shard]++
		row := Row{
			OrgID:     orgID,
			EventTime: base.Add(time.Duration(i) * time.Millisecond).Truncate(time.Microsecond),
			EventID:   uuid.Must(uuid.NewV7()),
			Seq:       seqByShard[shard],
			Shard:     shard,
			ActorID:   uuid.Must(uuid.NewV7()),
			ActorKind: 1,
			Action:    "auth.token_used",
			Outcome:   OutcomeOK,
			// A realistic context payload is the reason the old verifier grew
			// with bundle bytes: it held every one of these after decoding.
			Context: EventContext{
				OrgID:     orgID,
				RequestID: fmt.Sprintf("req-%d-scale-bundle-padding", i),
				TraceID:   fmt.Sprintf("trace-%d-scale-bundle-padding", i),
				Source:    SourceMCP,
			},
			PrevHash:    prevByShard[shard],
			HashVersion: auditHashVersion3,
		}
		row.RowHash = hashExportTestRow(t, row)
		prevByShard[shard] = row.RowHash
		rows = append(rows, row)
	}
	pub := writeSignedExportTestBundle(t, dir, rows)
	return dir, pub
}

// TestVerifyBundleMemoryStaysBounded pins the TACK-463 fix: verification's
// peak heap tracks the row count times a fixed-width link record, not the size
// of the bundle. The old verifier read events.jsonl whole and retained every
// decoded Row, so a 420,562-row production bundle was killed by the
// out-of-memory killer; this asserts the shape that made that impossible
// rather than asserting the one number that failed.
func TestVerifyBundleMemoryStaysBounded(t *testing.T) {
	dir, pub := buildScaleBundle(t, verifyScaleRows)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	// TotalAlloc never decreases, so this is allocation volume rather than a
	// live-heap sample that GC timing could flatter.
	allocatedDuringVerify := after.TotalAlloc - before.TotalAlloc
	peakHeap := after.HeapAlloc

	if report.RowsScanned != verifyScaleRows {
		t.Fatalf("rows scanned = %d, want %d", report.RowsScanned, verifyScaleRows)
	}
	if report.HashMatches != verifyScaleRows {
		t.Fatalf("hash matches = %d, want %d", report.HashMatches, verifyScaleRows)
	}
	if len(report.ChainBreaks) != 0 {
		t.Fatalf("chain breaks = %v, want none", report.ChainBreaks)
	}
	if !report.FileSHA256OK || !report.SignatureOK {
		t.Fatalf("digest ok = %v, signature ok = %v, want both true", report.FileSHA256OK, report.SignatureOK)
	}

	// The retained state is one chainLink per row. The headroom is deliberately
	// small: a budget loose enough to absorb a decoded Row per row would pass
	// against the very implementation that was killed in production, so this
	// allows four times the link record plus a few megabytes for the decoder's
	// buffers and slice growth, and nothing like the hundreds of bytes a
	// retained Row costs.
	const retainedBudgetPerRow = 4 * int(unsafeSizeofChainLink)
	const fixedHeadroomBytes = 4 << 20
	retainedBudget := uint64(verifyScaleRows*retainedBudgetPerRow) + fixedHeadroomBytes
	if peakHeap > retainedBudget {
		t.Fatalf("peak heap %d bytes exceeds the %d-byte budget for %d rows; verification is retaining more than its chain links",
			peakHeap, retainedBudget, verifyScaleRows)
	}

	// Extrapolate to the production bundle that failed: the same per-row cost
	// must clear the tightest container the command runs in.
	const productionRows = 420562
	projected := peakHeap / uint64(verifyScaleRows) * productionRows
	if projected > opsSidecarMemLimitBytes {
		t.Fatalf("projected peak for %d rows is %d bytes, over the %d-byte ops sidecar limit (measured %d bytes for %d rows); allocated %d bytes during verify",
			productionRows, projected, int(opsSidecarMemLimitBytes), peakHeap, verifyScaleRows, allocatedDuringVerify)
	}
	t.Logf("verified %d rows: peak heap %d bytes, allocated %d bytes, projected %d bytes at %d rows against a %d-byte limit",
		verifyScaleRows, peakHeap, allocatedDuringVerify, projected, productionRows, int(opsSidecarMemLimitBytes))
}

// unsafeSizeofChainLink is the fixed width of the record verification keeps
// per row: two 32-byte hashes, a 16-byte event id, an 8-byte sequence, and a
// 2-byte shard, rounded up for alignment.
const unsafeSizeofChainLink = 2*sha256.Size + 16 + 8 + 8
