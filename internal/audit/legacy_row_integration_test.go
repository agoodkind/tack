package audit

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestLegacyRowReadsAsUnrecordedAndStillVerifies is the whole point of the
// legacy writer. A row written before the ledger stored outcomes must read as
// unrecorded rather than as a success nobody observed, and it must still
// verify, because production's ledger is full of such rows and a verification
// that rejected them would report the honest past as tampered.
//
// Only production carries these rows naturally. This proves the contract
// against a real ledger by writing one.
func TestLegacyRowReadsAsUnrecordedAndStillVerifies(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()

	written, err := WriteLegacyRow(ctx, pool, LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Must(uuid.NewV7()),
	})
	if err != nil {
		t.Fatalf("write legacy row: %v", err)
	}

	stored, err := (&Reader{pool: pool}).GetByID(ctx, written.EventID)
	if err != nil {
		t.Fatalf("read the legacy row back: %v", err)
	}
	if stored.Outcome != OutcomeUnrecorded {
		t.Fatalf("outcome = %q, want %q; a missing outcome must never read as a result", stored.Outcome, OutcomeUnrecorded)
	}
	if stored.HashVersion != auditHashVersion1 {
		t.Fatalf("hash version = %d, want %d", stored.HashVersion, auditHashVersion1)
	}
	if stored.Error != nil {
		t.Fatalf("error = %+v, want it absent", stored.Error)
	}
	if len(stored.Extra) != 0 {
		t.Fatalf("extra = %s, want it absent", stored.Extra)
	}

	matches, reason, err := checkRowHash(*stored)
	if err != nil {
		t.Fatalf("check the row hash: %v", err)
	}
	if !matches {
		t.Fatalf("the legacy row does not verify: %s", reason)
	}
}

// TestLegacyRowExtendsTheChainItJoins pins that the fabricated row is a real
// member of its chain, not an island. A row whose prev_hash did not come from
// the chain head would break verification for every row after it.
func TestLegacyRowExtendsTheChainItJoins(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	input := LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Must(uuid.NewV7()),
	}

	first, err := WriteLegacyRow(ctx, pool, input)
	if err != nil {
		t.Fatalf("write the first legacy row: %v", err)
	}
	// The shard derives from the actor and event ids, so the second row picks
	// an event id that lands on the first row's chain.
	input.EventID = eventIDOnShard(t, input.ActorID, first.Shard)
	second, err := WriteLegacyRow(ctx, pool, input)
	if err != nil {
		t.Fatalf("write the second legacy row: %v", err)
	}
	if second.Shard != first.Shard {
		t.Fatalf("the second row landed on shard %d, want %d", second.Shard, first.Shard)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("sequences = %d then %d, want consecutive rows on one chain", first.Seq, second.Seq)
	}

	reader := &Reader{pool: pool}
	firstRow, err := reader.GetByID(ctx, first.EventID)
	if err != nil {
		t.Fatalf("read the first row: %v", err)
	}
	secondRow, err := reader.GetByID(ctx, second.EventID)
	if err != nil {
		t.Fatalf("read the second row: %v", err)
	}
	if !bytesEqual(secondRow.PrevHash, firstRow.RowHash) {
		t.Fatal("the second row does not hash on top of the first, so the chain is broken")
	}
	for _, row := range []*Row{firstRow, secondRow} {
		matches, reason, hashErr := checkRowHash(*row)
		if hashErr != nil {
			t.Fatalf("check row %s: %v", row.EventID, hashErr)
		}
		if !matches {
			t.Fatalf("row %s does not verify: %s", row.EventID, reason)
		}
	}
}

// eventIDOnShard returns a fresh event id that shardOf places on shard for
// actorID. One in 256 ids lands there, so the search is short.
func eventIDOnShard(t *testing.T, actorID uuid.UUID, shard int16) uuid.UUID {
	t.Helper()
	const attempts = 100000
	for range attempts {
		candidate := uuid.Must(uuid.NewV7())
		if shardOf(actorID, candidate) == shard {
			return candidate
		}
	}
	t.Fatalf("no event id landed on shard %d in %d attempts", shard, attempts)
	return uuid.Nil
}
