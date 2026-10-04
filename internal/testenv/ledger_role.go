package testenv

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExecAsRole uses an uncanceled context for cleanup statements.
// Test cleanup runs after the test context is canceled.
func ExecAsRole(tb testing.TB, pool *pgxpool.Pool, role, statement string) {
	tb.Helper()
	ctx := context.WithoutCancel(tb.Context())
	connection, err := pool.Acquire(ctx)
	if err != nil {
		tb.Fatalf("acquire a ledger connection: %v", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "SET ROLE "+role); err != nil {
		tb.Fatalf("set role %s: %v", role, err)
	}
	defer func() { _, _ = connection.Exec(ctx, "RESET ROLE") }()
	if _, err := connection.Exec(ctx, statement); err != nil {
		tb.Fatalf("%q as %s: %v", statement, role, err)
	}
}
