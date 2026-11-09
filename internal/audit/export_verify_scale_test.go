package audit

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

const opsSidecarMemLimitBytes = 512 << 20

const verifyScaleRows = 20000

// buildScaleBundle writes a signed bundle of n rows across shards. Each
// shard's rows form a chain that exercises link verification.
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

// TestVerifyBundleMultiShardIntegrity checks row counts, hashes, chains,
// the signature, and the file digest across all shards in a signed bundle.
func TestVerifyBundleMultiShardIntegrity(t *testing.T) {
	dir, pub := buildScaleBundle(t, verifyScaleRows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

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
}
