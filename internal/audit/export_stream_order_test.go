// export_stream_order_test.go covers row ordering and the chain verdict: the
// order the ledger hands rows over in does not change what the verifier says.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestExportRowOrderDoesNotChangeTheChainVerdict answers what streaming costs
// the chain check: nothing. A whole-range export carries no ORDER BY, so the
// database is free to return an org's rows in any order, including with a
// shard's rows split apart and its sequences descending. The verifier sorts
// what it scans into per-shard sequence order before it walks the chain, so the
// same rows in a different file order produce the same verdict: every row
// re-hashed, every link checked, no gap invented by the ordering.
func TestExportRowOrderDoesNotChangeTheChainVerdict(t *testing.T) {
	const shards, perShard = 6, 100
	orgID := uuid.Must(uuid.NewV7())
	chained := chainedLedgerRows(t, orgID, shards, perShard)
	scrambledRows := scrambleRowOrder(chained)
	// The order is the fixture this test rests on, so it is checked before it is
	// exported. A scramble that quietly left the shards in blocks would make
	// every assertion below pass for the wrong reason.
	assertShardsInterleaved(t, scrambledRows)

	inFileOrder := exportedVerifyReport(t, orgID, chained)
	scrambled := exportedVerifyReport(t, orgID, scrambledRows)

	for name, report := range map[string]*VerifyReport{"chain order": inFileOrder, "scrambled": scrambled} {
		if report.RowsScanned != shards*perShard {
			t.Fatalf("%s: rows scanned = %d, want %d", name, report.RowsScanned, shards*perShard)
		}
		if report.HashMatches != shards*perShard {
			t.Fatalf("%s: hash matches = %d, want %d", name, report.HashMatches, shards*perShard)
		}
		if len(report.ChainBreaks) != 0 {
			t.Fatalf("%s: chain breaks = %v, want none", name, report.ChainBreaks)
		}
		if report.ChainGapCount != 0 {
			t.Fatalf("%s: sequence gaps = %d, want 0; the row order must not invent a gap",
				name, report.ChainGapCount)
		}
		if verdict := report.Err(); verdict != nil {
			t.Fatalf("%s: %v", name, verdict)
		}
	}
}

// chainedLedgerRows builds perShard correctly linked rows for each shard.
func chainedLedgerRows(t *testing.T, orgID uuid.UUID, shards, perShard int) []Row {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	rows := make([]Row, 0, shards*perShard)
	for shard := range shards {
		var prevHash []byte
		for seq := 1; seq <= perShard; seq++ {
			row := Row{
				OrgID:      orgID,
				EventTime:  base.Add(time.Duration(shard*perShard+seq) * time.Millisecond).Truncate(time.Microsecond),
				EventID:    uuid.Must(uuid.NewV7()),
				Seq:        int64(seq),
				Shard:      int16(shard),
				ActorID:    uuid.Must(uuid.NewV7()),
				ActorKind:  5,
				Action:     "ops.test.export_order",
				Outcome:    OutcomeOK,
				EntityKind: "system", EntityID: orgID,
				Context:     EventContext{OrgID: orgID, Source: SourceSystem},
				PrevHash:    prevHash,
				HashVersion: auditHashVersion3,
			}
			row.RowHash = hashExportTestRow(t, row)
			prevHash = row.RowHash
			rows = append(rows, row)
		}
	}
	return rows
}

// scrambleRowOrder returns the same rows in an order no chain walk could use
// directly: the shards are interleaved, so no two consecutive rows come from
// the same one, and each shard's own sequence arrives rotated and reversed.
// This is the worst order a database with no ORDER BY could hand back.
//
// Reversing the whole slice instead leaves every shard in one contiguous block,
// because the rows are built shard-major. A bundle in that order is the shape
// the export already writes, so the verdict it earns says nothing about the
// interleaving this test names.
func scrambleRowOrder(rows []Row) []Row {
	pending := map[int16][]Row{}
	shardOrder := make([]int16, 0)
	for _, row := range rows {
		if _, seen := pending[row.Shard]; !seen {
			shardOrder = append(shardOrder, row.Shard)
		}
		pending[row.Shard] = append(pending[row.Shard], row)
	}
	for _, shard := range shardOrder {
		// A different rotation per shard so the shards are not phase-aligned
		// either: their sequences wrap at different points in the file.
		pending[shard] = rotatedReverse(pending[shard], int(shard)+1)
	}

	scrambled := make([]Row, 0, len(rows))
	for len(scrambled) < len(rows) {
		for _, shard := range shardOrder {
			remaining := pending[shard]
			if len(remaining) == 0 {
				continue
			}
			scrambled = append(scrambled, remaining[0])
			pending[shard] = remaining[1:]
		}
	}
	return scrambled
}

// rotatedReverse returns one shard's rows backwards and rotated by offset, so
// the sequence does not merely descend: it runs backwards, wraps once, and runs
// backwards again. A plain reversal is still monotonic, and monotonic is an
// order a chain walk can special-case its way through.
func rotatedReverse(rows []Row, offset int) []Row {
	if len(rows) == 0 {
		return rows
	}
	reversed := make([]Row, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		reversed = append(reversed, rows[i])
	}
	pivot := offset % len(reversed)
	rotated := make([]Row, 0, len(reversed))
	rotated = append(rotated, reversed[pivot:]...)
	rotated = append(rotated, reversed[:pivot]...)
	return rotated
}

// assertShardsInterleaved fails when the scrambled order still hands a shard's
// rows over in one block, or hands any shard over in sequence order. The order
// is the whole subject of the test that calls this: an order that keeps each
// shard contiguous and ascending is the order the export itself writes, and a
// verdict earned on that order says nothing about a database that interleaves.
func assertShardsInterleaved(t *testing.T, rows []Row) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		if rows[i].Shard == rows[i-1].Shard {
			t.Fatalf("rows %d and %d both come from shard %d, so the shards are not interleaved",
				i-1, i, rows[i].Shard)
		}
	}
	seqByShard := map[int16][]int64{}
	for _, row := range rows {
		seqByShard[row.Shard] = append(seqByShard[row.Shard], row.Seq)
	}
	for shard, sequences := range seqByShard {
		if slices.IsSorted(sequences) {
			t.Fatalf("shard %d arrives in sequence order, so the file order is one a chain walk could use directly",
				shard)
		}
	}
}

// exportedVerifyReport exports the rows in the order given and returns the
// verifier's report on the bundle, so the assertion is on what the production
// verifier says about a real bundle rather than on the fixture.
func exportedVerifyReport(t *testing.T, orgID uuid.UUID, rows []Row) *VerifyReport {
	t.Helper()
	dir := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest, err := Export(
		context.Background(), &sliceRowSource{rows: rows}, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != len(rows) {
		t.Fatalf("manifest row count = %d, want %d", manifest.RowCount, len(rows))
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return report
}
