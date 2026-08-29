package audit

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// purgeNilOrg empties the nil org's rows and heads so the counts below are
// exact. The throwaway database is disposable and shared across test runs,
// so leftovers from other suites would otherwise ride into this test's move.
func purgeNilOrg(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, uuid.Nil); err != nil {
		t.Fatalf("purge nil events: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, uuid.Nil); err != nil {
		t.Fatalf("purge nil heads: %v", err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, orgID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), query, orgID).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// chainRow is one (seq, prev_hash, row_hash) triple of a stored chain.
type chainRow struct {
	Seq      int64
	PrevHash []byte
	RowHash  []byte
}

// readChain returns one (org, shard) chain in seq order.
func readChain(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, shard int16) []chainRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT seq, prev_hash, row_hash FROM audit.events
		WHERE org_id = $1 AND shard = $2 ORDER BY seq
	`, orgID, shard)
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	defer rows.Close()
	var out []chainRow
	for rows.Next() {
		var row chainRow
		if err := rows.Scan(&row.Seq, &row.PrevHash, &row.RowHash); err != nil {
			t.Fatalf("scan chain: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("chain rows: %v", err)
	}
	return out
}

// assertChainLinks fails unless the chain is gapless from seq 1 and every
// prev_hash equals the prior row's hash, and the stored head matches the tip.
func assertChainLinks(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, shard int16, chain []chainRow) {
	t.Helper()
	for i, row := range chain {
		if row.Seq != int64(i+1) {
			t.Fatalf("shard %d seq[%d] = %d, want %d (gap or duplicate)", shard, i, row.Seq, i+1)
		}
		if i == 0 {
			continue
		}
		if !bytes.Equal(row.PrevHash, chain[i-1].RowHash) {
			t.Fatalf("shard %d chain break at seq %d", shard, row.Seq)
		}
	}
	tip := chain[len(chain)-1]
	var headSeq int64
	var headHash []byte
	err := pool.QueryRow(context.Background(), `
		SELECT last_seq, last_hash FROM audit.chain_heads WHERE org_id = $1 AND shard = $2
	`, orgID, shard).Scan(&headSeq, &headHash)
	if err != nil {
		t.Fatalf("shard %d head: %v", shard, err)
	}
	if headSeq != tip.Seq || !bytes.Equal(headHash, tip.RowHash) {
		t.Fatalf("shard %d head (%d) does not match tip (%d)", shard, headSeq, tip.Seq)
	}
}

// TestMoveNilOrgRowsAppendsAndVerifies is the TACK-461 acceptance: nil-org
// rows land on the target's chains with recomputed verifiable hashes, the
// target's pre-existing rows stay byte-identical, historical event times
// survive, the nil chains vanish, and a rerun moves nothing.
func TestMoveNilOrgRowsAppendsAndVerifies(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	purgeNilOrg(t, pool)
	const shardWithReal int16 = 21
	const shardNilOnly int16 = 22

	// Three rows per chunk forces the five-row shard through two chunk
	// transactions plus the drain pass, so the chunking is exercised rather
	// than skipped by a small corpus.
	savedChunk := moveChunkRows
	moveChunkRows = 3
	t.Cleanup(func() { moveChunkRows = savedChunk })

	for range 3 {
		if err := appendWithRetry(ctx, pool, chainTestEvent(t, orgID, shardWithReal)); err != nil {
			t.Fatalf("seed real row: %v", err)
		}
	}
	nilInputs := make([]chainAppendInput, 0, 7)
	for range 5 {
		nilInputs = append(nilInputs, chainTestEvent(t, uuid.Nil, shardWithReal))
	}
	for range 2 {
		nilInputs = append(nilInputs, chainTestEvent(t, uuid.Nil, shardNilOnly))
	}
	for _, in := range nilInputs {
		if err := appendWithRetry(ctx, pool, in); err != nil {
			t.Fatalf("seed nil row: %v", err)
		}
	}
	realBefore := readChain(t, pool, orgID, shardWithReal)

	backfill := &OrgBackfill{pool: pool}
	plan, err := backfill.PlanNilOrgMove(ctx)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.NilRows != 7 || plan.Shards != 2 {
		t.Fatalf("plan = %+v, want 7 rows over 2 shards", plan)
	}
	// A guard that refuses aborts before anything moves: the negative case.
	refusal := errors.New("premise gone")
	_, err = backfill.MoveNilOrgRows(ctx, orgID, func(context.Context, pgx.Tx, []Row) error { return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("refusing guard err = %v, want the guard's refusal", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit.events WHERE org_id = $1`, uuid.Nil); n != 7 {
		t.Fatalf("nil rows after refused move = %d, want the untouched 7", n)
	}

	guardRuns := 0
	guard := func(_ context.Context, _ pgx.Tx, chunk []Row) error {
		guardRuns++
		if len(chunk) > moveChunkRows {
			t.Fatalf("guard saw %d rows, want at most the %d-row chunk", len(chunk), moveChunkRows)
		}
		return nil
	}
	move, err := backfill.MoveNilOrgRows(ctx, orgID, guard)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if move.RowsMoved != 7 || move.ShardsTouched != 2 || move.Passes != 2 {
		t.Fatalf("move = %+v, want 7 rows over 2 shards in 2 passes", move)
	}
	if guardRuns < 4 {
		t.Fatalf("guard ran %d times, want one run per chunk transaction (at least 4)", guardRuns)
	}

	if n := countRows(t, pool, `SELECT count(*) FROM audit.events WHERE org_id = $1`, uuid.Nil); n != 0 {
		t.Fatalf("nil rows remaining = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit.chain_heads WHERE org_id = $1`, uuid.Nil); n != 0 {
		t.Fatalf("nil heads remaining = %d, want 0", n)
	}

	merged := readChain(t, pool, orgID, shardWithReal)
	if len(merged) != 8 {
		t.Fatalf("merged chain rows = %d, want 8", len(merged))
	}
	assertChainLinks(t, pool, orgID, shardWithReal, merged)
	assertChainLinks(t, pool, orgID, shardNilOnly, readChain(t, pool, orgID, shardNilOnly))
	for i, before := range realBefore {
		if !bytes.Equal(before.RowHash, merged[i].RowHash) || before.Seq != merged[i].Seq {
			t.Fatalf("pre-existing row %d changed: the move must never rewrite the target's rows", i)
		}
	}
	assertMovedRowsVerify(t, pool, orgID, nilInputs)

	again, err := backfill.MoveNilOrgRows(ctx, orgID, nil)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if again.RowsMoved != 0 {
		t.Fatalf("rerun moved %d rows, want 0", again.RowsMoved)
	}
}

// assertMovedRowsVerify reads each moved row back and demands the target org,
// the current hash version, the original event time, and a hash that
// recomputes from the stored row exactly as bundle verification does.
func assertMovedRowsVerify(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, seeded []chainAppendInput) {
	t.Helper()
	reader := &Reader{pool: pool}
	for _, in := range seeded {
		row, err := reader.GetByID(context.Background(), in.EventID)
		if err != nil {
			t.Fatalf("read back %s: %v", in.EventID, err)
		}
		if row.OrgID != orgID {
			t.Fatalf("row %s org = %s, want %s", in.EventID, row.OrgID, orgID)
		}
		if row.HashVersion != auditHashVersionCurrent {
			t.Fatalf("row %s hash version = %d, want %d", in.EventID, row.HashVersion, auditHashVersionCurrent)
		}
		wantTime := in.Event.OccurredAt.UTC().Truncate(time.Microsecond)
		if !row.EventTime.Equal(wantTime) {
			t.Fatalf("row %s event time = %s, want its historical %s", in.EventID, row.EventTime, wantTime)
		}
		ok, reason, err := checkRowHash(*row)
		if err != nil {
			t.Fatalf("verify %s: %v", in.EventID, err)
		}
		if !ok {
			t.Fatalf("moved row %s does not verify: %s", in.EventID, reason)
		}
	}
}
