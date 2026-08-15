package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
)

// chainTestDSNEnv names the DSN of a migrated audit database. The tests below
// validate the cutover's concurrency and idempotency guarantees against a real
// Postgres-compatible engine (YugabyteDB), and skip when it is unset.
const chainTestDSNEnv = "AUDIT_CHAIN_TEST_DSN"

func chainTestPool(t *testing.T) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv(chainTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated audit DSN to run", chainTestDSNEnv)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
		pool.Close()
	})
	return pool, orgID
}

func chainTestEvent(orgID uuid.UUID, shard int16) chainAppendInput {
	ev := Event{
		EventID:    uuid.Must(uuid.NewV7()),
		Verb:       string(VerbNodeRead),
		Actor:      Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:     Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context:    EventContext{OrgID: orgID, Source: SourceMCP},
		Outcome:    OutcomeOK,
		OccurredAt: clock.Now().UTC(),
	}
	return chainAppendInput{
		Event:       ev,
		EventID:     ev.EventID,
		Shard:       shard,
		PIIRef:      uuid.Nil,
		ContextJSON: []byte("{}"),
		DeltaJSON:   []byte("null"),
	}
}

// appendWithRetry mirrors the consumer/YBRecorder retry loop: a fresh
// transaction per attempt, retrying on a head conflict or serialization error.
func appendWithRetry(ctx context.Context, pool *pgxpool.Pool, in chainAppendInput) error {
	var lastErr error
	for attempt := 0; attempt < 64; attempt++ {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			lastErr = err
			continue
		}
		_, err = appendChainRow(ctx, tx, in)
		if err != nil {
			_ = tx.Rollback(ctx)
			lastErr = err
			if isRetryableChainErr(err) {
				continue
			}
			return err
		}
		err = tx.Commit(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableChainErr(err) {
			return err
		}
	}
	return lastErr
}

// TestAppendChainRowConcurrent is the core cutover criterion: many writers
// advancing the same (org, shard) chain at once produce one unbroken sequence
// with no forked or duplicate seq, and every prev_hash links the prior row.
func TestAppendChainRowConcurrent(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	const shard int16 = 7
	const writers = 32

	var wg sync.WaitGroup
	failures := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := appendWithRetry(ctx, pool, chainTestEvent(orgID, shard)); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent writer failed: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT seq, prev_hash, row_hash FROM audit.events
		WHERE org_id = $1 AND shard = $2 ORDER BY seq
	`, orgID, shard)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var seqs []int64
	var prevHashes, rowHashes [][]byte
	for rows.Next() {
		var seq int64
		var prev, row []byte
		if err := rows.Scan(&seq, &prev, &row); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seqs = append(seqs, seq)
		prevHashes = append(prevHashes, prev)
		rowHashes = append(rowHashes, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(seqs) != writers {
		t.Fatalf("got %d rows, want %d (chain forked or lost writes)", len(seqs), writers)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d (gap or duplicate)", i, seq, i+1)
		}
		if i == 0 {
			if len(prevHashes[0]) != 0 {
				t.Fatalf("first row prev_hash is not empty")
			}
			continue
		}
		if !bytes.Equal(prevHashes[i], rowHashes[i-1]) {
			t.Fatalf("chain break at seq %d: prev_hash does not match prior row_hash", seq)
		}
	}
}

// TestAppendChainRowHashRecomputableFromStoredRow is the TACK-445 acceptance:
// an event written with a nanosecond OccurredAt must verify from what the
// database actually stored. The event goes through the real writer and the
// real column, then through Export and VerifyBundle exactly as the operator
// runs them, so every hash input comes from the read-back row and none from
// the original event. A hash that covers anything the column truncates, or an
// export mapping that drops a hashed field, fails here.
func TestAppendChainRowHashRecomputableFromStoredRow(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	const shard int16 = 11
	in := chainTestEvent(orgID, shard)
	// A deterministic remainder, so the round trip always exercises truncation
	// and the recomputation is reproducible run to run.
	in.Event.OccurredAt = in.Event.OccurredAt.Truncate(time.Microsecond).Add(789 * time.Nanosecond)

	if err := appendWithRetry(ctx, pool, in); err != nil {
		t.Fatalf("append: %v", err)
	}

	reader := &Reader{pool: pool}
	row, err := reader.GetByID(ctx, in.EventID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.HashVersion != auditHashVersionCurrent {
		t.Fatalf("stored hash_version = %d, want %d", row.HashVersion, auditHashVersionCurrent)
	}
	if row.EventTime.Nanosecond()%1000 != 0 {
		t.Fatalf("stored event_time %v carries a sub-microsecond remainder; the column contract changed", row.EventTime)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	filter := QueryFilter{
		OrgID: orgID, Oldest: in.Event.OccurredAt.Add(-time.Minute), Latest: in.Event.OccurredAt.Add(time.Minute),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 10,
	}
	manifest, err := Export(ctx, reader, priv, "ed25519:test", filter, dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != 1 {
		t.Fatalf("exported %d rows, want 1", manifest.RowCount)
	}
	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("honest bundle exported from the database rejected: %v", verdict)
	}
	if report.HashMatches != 1 {
		t.Fatalf("hash matches = %d, want 1; the stored row_hash does not recompute from the exported row", report.HashMatches)
	}
}

// TestAppendChainRowIdempotent confirms a redelivered event (same event_id and
// event_time) is rejected by the unique index and never advances the chain a
// second time.
func TestAppendChainRowIdempotent(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	const shard int16 = 9
	in := chainTestEvent(orgID, shard)

	if err := appendWithRetry(ctx, pool, in); err != nil {
		t.Fatalf("first append: %v", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = appendChainRow(ctx, tx, in)
	if err != errAlreadyProjected {
		t.Fatalf("redelivered append returned %v, want errAlreadyProjected", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit.events WHERE org_id = $1 AND shard = $2
	`, orgID, shard).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("got %d rows after redelivery, want 1", count)
	}
}
