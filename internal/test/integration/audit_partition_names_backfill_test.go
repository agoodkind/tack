package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const auditGuardMigration = 17

func TestAuditPartitionNamesBackfillUnblocksMaintenance(t *testing.T) {
	node := testenv.StartEmptyLedger(t, fmt.Sprintf("tack-testenv-yugabyte-partition-names-%d", os.Getpid()))
	migrateLedgerTo(t, node.DSN, auditGuardMigration-1)
	pool, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE audit.events_tack336_proof PARTITION OF audit.events
		FOR VALUES FROM ('2031-03-03 00:00:00+00') TO ('2031-03-10 00:00:00+00')`); err != nil {
		t.Fatalf("create the misnamed child: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT audit.run_partition_maintenance()`); err == nil || !strings.Contains(err.Error(), "roof") {
		t.Fatalf("maintenance with the misnamed child = %v, want the pg_partman date error", err)
	}
	if err := migrateLedgerUpTo(t.Context(), node.DSN, auditGuardMigration); err == nil || !strings.Contains(err.Error(), "events_tack336_proof") {
		t.Fatalf("migration 017 with the misnamed child = %v, want a refusal that lists the child", err)
	}

	planned, err := ops.RunAuditPartitionNamesBackfill(t.Context(), pool, true)
	if err != nil || len(planned.Renames) != 1 || planned.Renames[0].To != "events_p2031_03_03" {
		t.Fatalf("dry run = %+v, %v; want one rename to events_p2031_03_03", planned, err)
	}
	if !childExists(t, pool, "events_tack336_proof") {
		t.Fatal("the dry run renamed the child")
	}
	if _, err := ops.RunAuditPartitionNamesBackfill(t.Context(), pool, false); err != nil {
		t.Fatalf("execute the backfill: %v", err)
	}
	if childExists(t, pool, "events_tack336_proof") || !childExists(t, pool, "events_p2031_03_03") {
		t.Fatal("the child was not renamed to events_p2031_03_03")
	}
	var key string
	if err := pool.QueryRow(t.Context(), `SELECT conname FROM pg_constraint
		WHERE conrelid = 'audit.events_p2031_03_03'::regclass AND contype = 'p'`).Scan(&key); err != nil || key != "events_p2031_03_03_pkey" {
		t.Fatalf("primary key of the renamed child = %q, %v; want events_p2031_03_03_pkey", key, err)
	}

	migrateLedgerTo(t, node.DSN, auditGuardMigration)
	if _, err := pool.Exec(t.Context(), `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("maintenance after the rename: %v", err)
	}
	monday := time.Now().UTC().Truncate(24 * time.Hour)
	for monday.Weekday() != time.Monday {
		monday = monday.Add(-24 * time.Hour)
	}
	for week := range 3 {
		name := "events_p" + monday.Add(time.Duration(week)*7*24*time.Hour).Format("2006_01_02")
		if !childExists(t, pool, name) {
			t.Errorf("maintenance did not create %s", name)
		}
	}
	rerun, err := ops.RunAuditPartitionNamesBackfill(t.Context(), pool, true)
	if err != nil || len(rerun.Renames) != 0 {
		t.Fatalf("second run = %+v, %v; want no rename", rerun, err)
	}
}

func migrateLedgerTo(t *testing.T, dsn string, version int64) {
	t.Helper()
	if err := migrateLedgerUpTo(t.Context(), dsn, version); err != nil {
		t.Fatalf("migrate the ledger to %d: %v", version, err)
	}
}

func migrateLedgerUpTo(ctx context.Context, dsn string, version int64) error {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open the ledger: %w", err)
	}
	defer func() { _ = database.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.UpTo(ctx, version); err != nil {
		return fmt.Errorf("migrate up to %d: %w", version, err)
	}
	return nil
}

func childExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events' AND c.relname = $1`, name).Scan(&count); err != nil {
		t.Fatalf("read the children of audit.events: %v", err)
	}
	return count == 1
}
