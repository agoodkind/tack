package audit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

const (
	guardRefusalSQLState = "42501"
	// A statement of this form broke QA partition maintenance (TACK-551).
	strayChildStatement = `CREATE TABLE audit.events_tack556_proof PARTITION OF audit.events
		FOR VALUES FROM ('2032-03-01 00:00:00+00') TO ('2032-03-08 00:00:00+00')`
	guardPartitionCountQuery = `SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace WHERE n.nspname = 'audit' AND p.relname = 'events'`
)

// TestSchemaGuardRefusesHandWrittenAuditDDL connects as the engine superuser
// (TACK-556).
func TestSchemaGuardRefusesHandWrittenAuditDDL(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testenv.Ledger(t))
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := audit.NewPGPartitionStore(pool)

	testenv.ExecAsRole(t, pool, postgres.MigratorRole, "CREATE TABLE audit.tack556_drop_probe (id int)")
	t.Cleanup(func() {
		testenv.ExecAsRole(t, pool, postgres.MigratorRole, "DROP TABLE IF EXISTS audit.tack556_drop_probe")
	})
	for _, statement := range []string{
		strayChildStatement,
		"ALTER TABLE audit.chain_heads ADD COLUMN tack556_refused int",
		"CREATE TABLE partman.tack556_refused (id int)",
		"DROP TABLE audit.tack556_drop_probe",
	} {
		_, err := pool.Exec(ctx, statement)
		var refused *pgconn.PgError
		if !errors.As(err, &refused) || refused.Code != guardRefusalSQLState ||
			!strings.Contains(refused.Message, "schema change refused") {
			t.Fatalf("%q: err = %v, want the schema guard refusal", statement, err)
		}
		if !strings.Contains(refused.Detail, "session role yugabyte") || !strings.Contains(refused.Detail, "statement:") {
			t.Fatalf("%q: detail = %q, want the session role and the statement", statement, refused.Detail)
		}
	}
	if strays, err := store.StrayChildren(ctx); err != nil || len(strays) != 0 {
		t.Fatalf("stray children after the refusals = %v, err = %v; want none", strays, err)
	}

	if _, err := pool.Exec(ctx, "CREATE TABLE public.tack556_probe (id int)"); err != nil {
		t.Fatalf("a public schema change must run: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TABLE public.tack556_probe"); err != nil {
		t.Fatalf("drop the public probe: %v", err)
	}

	before := guardPartitionCount(ctx, t, pool)
	if _, err := pool.Exec(ctx,
		`UPDATE partman.part_config SET premake = premake + 1 WHERE parent_table = 'audit.events'`); err != nil {
		t.Fatalf("raise the pg_partman premake: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE partman.part_config SET premake = premake - 1 WHERE parent_table = 'audit.events'`)
	})
	if err := store.RunMaintenance(ctx); err != nil {
		t.Fatalf("partition maintenance under the guard: %v", err)
	}
	if after := guardPartitionCount(ctx, t, pool); after <= before {
		t.Fatalf("partitions of audit.events = %d after maintenance, want more than %d", after, before)
	}

	setGuardTriggers(ctx, t, pool, "DISABLE")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS audit.events_tack556_proof")
		setGuardTriggers(ctx, t, pool, "ENABLE")
	})
	if _, err := pool.Exec(ctx, strayChildStatement); err != nil {
		t.Fatalf("create the stray child with the triggers disabled: %v", err)
	}
	strays, err := store.StrayChildren(ctx)
	if err != nil || !slices.Equal(strays, []string{"events_tack556_proof"}) {
		t.Fatalf("stray children = %v, err = %v; want events_tack556_proof", strays, err)
	}
}

func setGuardTriggers(ctx context.Context, t *testing.T, pool *pgxpool.Pool, state string) {
	t.Helper()
	for _, trigger := range []string{"tack_audit_schema_guard", "tack_audit_schema_guard_drop"} {
		if _, err := pool.Exec(ctx, "ALTER EVENT TRIGGER "+trigger+" "+state); err != nil {
			t.Fatalf("%s event trigger %s: %v", state, trigger, err)
		}
	}
}

func guardPartitionCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, guardPartitionCountQuery).Scan(&count); err != nil {
		t.Fatalf("count the partitions of audit.events: %v", err)
	}
	return count
}
