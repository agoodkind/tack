//go:build integration

package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// partmanTestDSN names a migrated audit database (post-004) reachable for the
// adoption test. Skips when unset, mirroring chainTestDSNEnv.
const partmanTestDSN = "AUDIT_CHAIN_TEST_DSN"

func partmanTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(partmanTestDSN)
	if dsn == "" {
		t.Skipf("set %s to a migrated audit DSN to run", partmanTestDSN)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestPartmanAdoptionCreatesFutureWeek verifies migration 004 registered
// audit.events with pg_partman, that the maintenance wrapper is callable, and
// that after maintenance a partition exists whose range covers a future week,
// with no overlap error. This is the adoption regression.
func TestPartmanAdoptionCreatesFutureWeek(t *testing.T) {
	pool := partmanTestPool(t)
	ctx := context.Background()

	var registered int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM partman.part_config WHERE parent_table = 'audit.events'`,
	).Scan(&registered); err != nil {
		t.Fatalf("part_config query: %v", err)
	}
	if registered != 1 {
		t.Fatalf("audit.events registered in part_config = %d, want 1", registered)
	}

	var retention *string
	if err := pool.QueryRow(ctx,
		`SELECT retention FROM partman.part_config WHERE parent_table = 'audit.events'`,
	).Scan(&retention); err != nil {
		t.Fatalf("retention query: %v", err)
	}
	if retention != nil {
		t.Fatalf("retention = %q, want NULL (never auto-drop audit data)", *retention)
	}

	if _, err := pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("run_partition_maintenance: %v", err)
	}

	future := time.Now().UTC().AddDate(0, 0, 56)
	var covering int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		AND $1::timestamptz >= (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz
		AND $1::timestamptz <  (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([0-9 :.+-]+)'''))[1]::timestamptz
	`, future).Scan(&covering); err != nil {
		t.Fatalf("covering-partition query: %v", err)
	}
	if covering != 1 {
		t.Fatalf("partitions covering %s = %d, want 1", future.Format("2006-01-02"), covering)
	}
}
