package audit

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLegacyRowReportsUnrecordedOutcomeAndVerifiesHash checks historical rows
// without outcome columns against a real ledger. Missing outcomes must not
// imply success or invalidate historical hashes.
func TestLegacyRowReportsUnrecordedOutcomeAndVerifiesHash(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()

	written, err := WriteLegacyRow(
		ctx,
		pool,
		LegacyRowInput{
			OrgID:    orgID,
			ActorID:  uuid.Must(uuid.NewV7()),
			EntityID: uuid.Must(uuid.NewV7()),
		},
	)
	if err != nil {
		t.Fatalf("write legacy row: %v", err)
	}

	stored, err := openTestReader(t, pool).GetByID(ctx, written.EventID)
	if err != nil {
		t.Fatalf("read the legacy row back: %v", err)
	}

	if stored.Outcome != OutcomeUnrecorded {
		t.Fatalf(
			"outcome = %q, want %q; a missing outcome must never read as a result",
			stored.Outcome,
			OutcomeUnrecorded,
		)
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

func openTestReader(t *testing.T, pool *pgxpool.Pool) *Reader {
	t.Helper()
	reader, err := NewReader(t.Context(), pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the reader: %v", err)
	}

	t.Cleanup(reader.Close)

	return reader
}

// TestLegacyRowsUseConsecutiveChainLinks checks consecutive sequences and
// predecessor hashes for two legacy rows on one shard.
func TestLegacyRowsUseConsecutiveChainLinks(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	input := LegacyRowInput{
		OrgID:    orgID,
		ActorID:  uuid.Must(uuid.NewV7()),
		EntityID: uuid.Must(uuid.NewV7()),
	}

	first, err := WriteLegacyRow(ctx, pool, input)
	if err != nil {
		t.Fatalf("write the first legacy row: %v", err)
	}

	// Select an event ID that hashes to the first row's shard for this actor.
	input.EventID = eventIDOnShard(t, input.ActorID, first.Shard)
	second, err := WriteLegacyRow(ctx, pool, input)
	if err != nil {
		t.Fatalf("write the second legacy row: %v", err)
	}

	if second.Shard != first.Shard {
		t.Fatalf("the second row uses shard %d, want %d", second.Shard, first.Shard)
	}

	if second.Seq != first.Seq+1 {
		t.Fatalf(
			"sequences = %d then %d, want consecutive rows on one chain",
			first.Seq,
			second.Seq,
		)
	}

	reader := openTestReader(t, pool)
	firstRow, err := reader.GetByID(ctx, first.EventID)
	if err != nil {
		t.Fatalf("read the first row: %v", err)
	}

	secondRow, err := reader.GetByID(ctx, second.EventID)
	if err != nil {
		t.Fatalf("read the second row: %v", err)
	}

	if !bytesEqual(secondRow.PrevHash, firstRow.RowHash) {
		t.Fatal("the second row's predecessor hash does not match the first row's hash")
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

// eventIDOnShard searches for a fresh event ID with the requested shard for actorID.
func eventIDOnShard(t *testing.T, actorID uuid.UUID, shard int16) uuid.UUID {
	t.Helper()

	const attempts = 100000
	for range attempts {
		candidate := uuid.Must(uuid.NewV7())
		if shardOf(actorID, candidate) == shard {
			return candidate
		}
	}

	t.Fatalf("event ID search did not match shard %d in %d attempts", shard, attempts)

	return uuid.Nil
}
