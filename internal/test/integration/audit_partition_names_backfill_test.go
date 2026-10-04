package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const auditGuardMigration = 17

func TestAuditPartitionNamesBackfillUnblocksMaintenance(t *testing.T) {
	dsn := emptyLedgerDatabase(t)
	migrateLedgerTo(t, dsn, auditGuardMigration-1)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, statement := range []string{
		`CREATE TABLE audit.events_tack336_proof PARTITION OF audit.events
			FOR VALUES FROM ('2031-03-03 00:00:00+00') TO ('2031-03-10 00:00:00+00')`,
		`CREATE TABLE audit.events_hand_week PARTITION OF audit.events
			FOR VALUES FROM ('2031-03-10 00:00:00+00') TO ('2031-03-17 00:00:00+00')`,
		`ALTER TABLE audit.events_hand_week RENAME CONSTRAINT events_hand_week_pkey TO hand_week_key`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatalf("create the misnamed children: %v", err)
		}
	}
	renamed := map[string]string{"events_tack336_proof": "events_p2031_03_03", "events_hand_week": "events_p2031_03_10"}
	if _, err := pool.Exec(t.Context(), `SELECT audit.run_partition_maintenance()`); err == nil || !strings.Contains(err.Error(), "roof") {
		t.Fatalf("maintenance with the misnamed child = %v, want the pg_partman date error", err)
	}
	if err := migrateLedgerUpTo(t.Context(), dsn, auditGuardMigration); err == nil || !strings.Contains(err.Error(), "events_tack336_proof") {
		t.Fatalf("migration 017 with the misnamed child = %v, want a refusal that lists the child", err)
	}

	planned, err := ops.RunAuditPartitionNamesBackfill(t.Context(), pool, true)
	if err != nil || len(planned.Renames) != len(renamed) {
		t.Fatalf("dry run = %+v, %v; want %d renames", planned, err, len(renamed))
	}
	for _, rename := range planned.Renames {
		if renamed[rename.From] != rename.To || !childExists(t, pool, rename.From) {
			t.Fatalf("dry run rename %+v; want %s left in place and planned as %s", rename, rename.From, renamed[rename.From])
		}
	}
	if _, err := ops.RunAuditPartitionNamesBackfill(t.Context(), pool, false); err != nil {
		t.Fatalf("execute the backfill: %v", err)
	}
	for from, to := range renamed {
		if childExists(t, pool, from) || !childExists(t, pool, to) {
			t.Fatalf("child %s was not renamed to %s", from, to)
		}
		var key string
		if err := pool.QueryRow(t.Context(), `SELECT conname FROM pg_constraint
			WHERE conrelid = ('audit.' || $1)::regclass AND contype = 'p'`, to).Scan(&key); err != nil || key != to+"_pkey" {
			t.Fatalf("primary key of %s = %q, %v; want %s_pkey", to, key, err, to)
		}
	}

	migrateLedgerTo(t, dsn, auditGuardMigration)
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

// emptyLedgerDatabase creates an unmigrated database on the process ledger
// and drops it after the test.
func emptyLedgerDatabase(t *testing.T) string {
	t.Helper()
	baseDSN := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), baseDSN)
	if err != nil {
		t.Fatalf("open the ledger admin pool: %v", err)
	}
	name := "tack_partition_names_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0", pgx.Identifier{name}.Sanitize())); err != nil {
		admin.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{name}.Sanitize())); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		admin.Close()
	})
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.Path = "/" + name
	return parsed.String()
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
