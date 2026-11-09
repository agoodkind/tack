// These tests check that ledger row order does not change the verifier's chain
// verdict.

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

// TestExportRowOrderDoesNotChangeTheChainVerdict checks equal verdicts for
// identical rows in different file orders. An unlimited ledger query omits
// ORDER BY, and the database may interleave shards or return descending
// sequences. The verifier sorts links by shard and sequence before checking the
// chain. Both orders must verify every row hash and link without introducing
// sequence gaps.
func TestExportRowOrderDoesNotChangeTheChainVerdict(t *testing.T) {
	const shards, perShard = 6, 100
	orgID := uuid.Must(uuid.NewV7())
	chained := chainedLedgerRows(t, orgID, shards, perShard)
	scrambledRows := scrambleRowOrder(chained)
	// Check the scrambled order before exporting it. An unchanged sequence of
	// contiguous shard blocks would let the test pass without exercising
	// interleaved shards.
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

// scrambleRowOrder interleaves shards without placing two rows from one shard
// consecutively. It also rotates and reverses each shard's sequence. This order
// exercises database output that a direct sequence walk cannot process
// correctly.
//
// The input groups rows by shard. Reversing that entire slice would leave
// contiguous shard blocks and would not exercise interleaving, even if
// verification passed.
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
		// Rotate each shard by a different offset. Their sequences then wrap at
		// different positions in the file.
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

// rotatedReverse reverses one shard's rows and rotates them by offset. The
// sequence descends, wraps once, and descends again. A plain reversal would
// remain monotonic and could let a special-case chain walk pass.
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

// assertShardsInterleaved rejects adjacent rows from the same shard and
// ascending sequences within any shard. Contiguous ascending shard blocks would
// not exercise the database interleaving that the export ordering test
// requires.
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

// exportedVerifyReport exports the supplied rows in their given order and
// returns the production verifier's report for that real bundle. The caller
// asserts verifier behavior rather than fixture properties alone.
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
