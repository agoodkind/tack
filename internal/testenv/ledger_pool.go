package testenv

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LedgerPool opens a connection pool on the ledger at dsn and closes it when
// the test ends. It takes a [testing.TB] because [T] has no Cleanup.
func LedgerPool(tb testing.TB, dsn string) *pgxpool.Pool {
	tb.Helper()
	pool, err := pgxpool.New(tb.Context(), dsn)
	if err != nil {
		tb.Fatalf("open the ledger pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	return pool
}
