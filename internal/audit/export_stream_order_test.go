// export_stream_order_test.go checks equal chain verdicts across ledger row orders.

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

// TestExportRowOrderDoesNotChangeTheChainVerdict compares ordered rows with
// interleaved, reversed, and rotated shard sequences. Whole-range exports do
// not request ORDER BY; the verifier must sort per-shard links before checking.
func TestExportRowOrderDoesNotChangeTheChainVerdict(t *testing.T) {
	const shards, perShard = 6, 100
	orgID := uuid.Must(uuid.NewV7())
	chained := chainedLedgerRows(t, orgID, shards, perShard)
	scrambledRows := scrambleRowOrder(chained)
	// Verify fixture ordering before exporting to exclude an ineffective scramble.
	assertShardsInterleaved(t, scrambledRows)

	inFileOrder := exportedVerifyReport(t, orgID, chained)
	scrambled := exportedVerifyReport(t, orgID, scrambledRows)

	for name, report := range map[string]*VerifyReport{
		"chain order": inFileOrder,
		"scrambled":   scrambled,
	} {
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
			t.Fatalf(
				"%s: sequence gaps = %d, want 0; the row order must not invent a gap",
				name,
				report.ChainGapCount,
			)
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
				OrgID: orgID,
				EventTime: base.Add(
					time.Duration(shard*perShard+seq) * time.Millisecond,
				).Truncate(
					time.Microsecond,
				),
				EventID:     uuid.Must(uuid.NewV7()),
				Seq:         int64(seq),
				Shard:       int16(shard),
				ActorID:     uuid.Must(uuid.NewV7()),
				ActorKind:   5,
				Action:      "ops.test.export_order",
				Outcome:     OutcomeOK,
				EntityKind:  "system",
				EntityID:    orgID,
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

// scrambleRowOrder interleaves shards and reverses and rotates each shard's
// sequences. Reversing the whole shard-major slice alone would leave shard
// blocks intact and would not exercise interleaving.
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
		// Use distinct rotation offsets to vary sequence wrap positions.
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

// rotatedReverse reverses a shard's sequence and rotates it by offset to test
// an order that does not simply ascend or descend.
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

// assertShardsInterleaved rejects consecutive rows from one shard and any
// shard with sorted sequences before testing the production verifier.
func assertShardsInterleaved(t *testing.T, rows []Row) {
	t.Helper()

	for i := 1; i < len(rows); i++ {
		if rows[i].Shard == rows[i-1].Shard {
			t.Fatalf(
				"rows %d and %d both use shard %d instead of interleaving",
				i-1,
				i,
				rows[i].Shard,
			)
		}
	}

	seqByShard := map[int16][]int64{}

	for _, row := range rows {
		seqByShard[row.Shard] = append(seqByShard[row.Shard], row.Seq)
	}

	for shard, sequences := range seqByShard {
		if slices.IsSorted(sequences) {
			t.Fatalf("shard %d retains sorted sequences instead of scrambled order", shard)
		}
	}
}

// exportedVerifyReport exports the supplied order and returns the production
// verifier's report for the resulting bundle.
func exportedVerifyReport(t *testing.T, orgID uuid.UUID, rows []Row) *VerifyReport {
	t.Helper()
	dir := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	manifest, err := Export(
		context.Background(),
		&sliceRowSource{rows: rows},
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
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
