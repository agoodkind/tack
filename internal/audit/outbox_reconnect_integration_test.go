package audit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

// TestWriteOutboxSurvivesALostConnection reproduces TACK-497 with a real
// ledger. The server terminates the outbox connection between writes,
// matching the connection loss when ops ledger node-prepare stops its own
// node. The second write must succeed.
func TestWriteOutboxSurvivesALostConnection(t *testing.T) {
	dsn := testenv.Ledger(t)
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}

	// The pool permits one connection. The test requires a successful write
	// after the server terminates that backend.
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}

	before, after := outboxTestEvent("before kill"), outboxTestEvent("after kill")
	t.Cleanup(func() {
		for _, event := range []Event{before, after} {
			_, _ = admin.Exec(
				ctx,
				`DELETE FROM public.ops_outbox WHERE event_id = $1`,
				event.EventID,
			)
		}

		admin.Close()
		pool.Close()
	})

	outbox := NewPoolOutbox(pool)
	if err := outbox.WriteOutbox(ctx, before); err != nil {
		t.Fatalf("write before kill: %v", err)
	}

	var backendPID int32
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatalf("read backend pid: %v", err)
	}

	var terminated bool
	if err := admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(
		&terminated,
	); err != nil ||
		!terminated {
		t.Fatalf("terminate backend %d: terminated=%v err=%v", backendPID, terminated, err)
	}

	if err := outbox.WriteOutbox(ctx, after); err != nil {
		t.Fatalf("write after the connection was killed: %v", err)
	}

	var count int
	if err := admin.QueryRow(
		ctx,
		`SELECT count(*) FROM public.ops_outbox WHERE event_id IN ($1, $2)`,
		before.EventID,
		after.EventID,
	).Scan(
		&count,
	); err != nil {
		t.Fatalf("count rows: %v", err)
	}

	if count != 2 {
		t.Fatalf("outbox holds %d of the two events, want both", count)
	}
}
