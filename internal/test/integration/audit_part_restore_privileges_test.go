package integration_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const (
	partRestorePreviousMigration = 26
	partRestoreMigration         = 27
	partRestoreRestrictedTables  = 4
)

func requireOperatorTablePrivileges(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(t.Context(), readTestSQL(t, "part_restore_table_privileges.sql"))
	if err != nil {
		t.Fatalf("read the table privileges of audit_operator: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table string
		var granted bool
		if err := rows.Scan(&table, &granted); err != nil {
			t.Fatalf("scan a table privilege: %v", err)
		}
		if granted {
			t.Fatalf("audit_operator has a table privilege on %s, want none", table)
		}
		seen++
	}
	if rows.Err() != nil || seen != partRestoreRestrictedTables {
		t.Fatalf("table privilege rows = %d, error %v, want %d", seen, rows.Err(), partRestoreRestrictedTables)
	}
}

func TestAuditPartRestoreMigrationGrantsOnlyExecute(t *testing.T) {
	dsn := emptyLedgerDatabase(t)
	if err := migration.MigrateTo(t.Context(), dsn, migrations.FS, partRestorePreviousMigration); err != nil {
		t.Fatalf("migrate to %d: %v", partRestorePreviousMigration, err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open the fresh ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	requireOperatorTablePrivileges(t, pool)
	if err := migration.MigrateTo(t.Context(), dsn, migrations.FS, partRestoreMigration); err != nil {
		t.Fatalf("migrate to %d: %v", partRestoreMigration, err)
	}
	requireOperatorTablePrivileges(t, pool)

	var securityDefiner, hasACL bool
	var owner string
	var config, executors []string
	if err := pool.QueryRow(t.Context(), readTestSQL(t, "part_restore_catalog.sql")).Scan(&securityDefiner, &owner, &config, &executors, &hasACL); err != nil {
		t.Fatalf("read the function catalog entry: %v", err)
	}
	slices.Sort(executors)
	if !securityDefiner || owner != "audit_writer" || !hasACL || !slices.Equal(executors, []string{"audit_operator", "audit_writer"}) ||
		!slices.Contains(config, "search_path=pg_catalog, audit, public") {
		t.Fatalf("function: security definer %v, owner %s, config %v, executors %v, has ACL %v", securityDefiner, owner, config, executors, hasACL)
	}
	var writerSelectsEvents, writerExecutesOutbox bool
	if err := pool.QueryRow(t.Context(), `SELECT has_table_privilege('audit_writer', 'audit.events', 'SELECT'),
		has_function_privilege('audit_writer', 'public.ops_outbox_write_encoded(uuid, jsonb, bytea[])', 'EXECUTE')`).Scan(&writerSelectsEvents, &writerExecutesOutbox); err != nil {
		t.Fatalf("read the audit_writer privileges: %v", err)
	}
	if !writerSelectsEvents || !writerExecutesOutbox {
		t.Fatalf("audit_writer: SELECT on audit.events %v, EXECUTE on ops_outbox_write_encoded %v, want both", writerSelectsEvents, writerExecutesOutbox)
	}
}

func TestAuditPartRestoreOperatorCannotTouchTables(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	fixture := newPartRestoreRawFixture(t, admin)
	_, err := operator.Exec(t.Context(), readTestSQL(t, "part_restore_part_insert.sql"),
		fixture.org, fixture.target, fixture.call.root, fixture.payload)
	requirePGCode(t, err, "42501")
	for _, table := range []string{"audit.event_parts", "audit.events"} {
		_, err := operator.Exec(t.Context(), "SELECT count(*) FROM "+table)
		requirePGCode(t, err, "42501")
	}
}

func TestAuditPartRestoreWritesOnePartAndOneOutboxRow(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	fixture := newPartRestoreRawFixture(t, admin)
	before := readPartRestoreCounts(t, admin, fixture.call.changeID)
	if err := fixture.call.run(t, operator); err != nil {
		t.Fatalf("restore call = %v, want no error", err)
	}
	after := readPartRestoreCounts(t, admin, fixture.call.changeID)
	want := before
	want.parts++
	want.outbox++
	if after != want {
		t.Fatalf("counts after the call = %+v, want %+v (one part and one outbox row)", after, want)
	}
	var digest string
	var payload []byte
	if err := admin.QueryRow(t.Context(), `SELECT digest, payload FROM audit.event_parts WHERE org_id = $1 AND event_id = $2 AND part_index = 0`,
		fixture.org, fixture.target).Scan(&digest, &payload); err != nil || digest != fixture.call.root || !bytes.Equal(payload, fixture.payload) {
		t.Fatalf("restored part = %q, %d bytes, error %v, want the source part", digest, len(payload), err)
	}
	requirePartRestoreRefusal(t, fixture.call.run(t, operator), "already has")
}
