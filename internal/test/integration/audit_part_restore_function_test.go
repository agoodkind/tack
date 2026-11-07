package integration_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/google/uuid"
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
	inserted, err := fixture.call.run(t, operator)
	if err != nil || inserted != 1 {
		t.Fatalf("restore call = %d, %v, want 1 and no error", inserted, err)
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
	_, err = fixture.call.run(t, operator)
	requirePartRestoreRefusal(t, err, "already has")
}

func TestAuditPartRestoreRefusals(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	otherOrg := uuid.Must(uuid.NewV7())
	cases := []struct {
		name     string
		prepare  func(t *testing.T, fixture partRestoreRawFixture)
		mutate   func(call *partRestoreCall)
		fragment string
	}{
		{name: "part index is not zero", mutate: func(c *partRestoreCall) { c.partIndex = 1 }, fragment: "is not 0"},
		{name: "source is the target", mutate: func(c *partRestoreCall) { c.source = c.event }, fragment: "source event is the target"},
		{name: "target does not exist", mutate: func(c *partRestoreCall) { c.event = uuid.Must(uuid.NewV7()) }, fragment: "target event does not match"},
		{name: "target row hash differs", mutate: func(c *partRestoreCall) { c.rowHash = []byte("different") }, fragment: "target event does not match"},
		{name: "target seq differs", mutate: func(c *partRestoreCall) { c.seq++ }, fragment: "target event does not match"},
		{name: "target shard differs", mutate: func(c *partRestoreCall) { c.shard++ }, fragment: "target event does not match"},
		{name: "target in another org", mutate: func(c *partRestoreCall) { c.org = otherOrg }, fragment: "target event does not match"},
		{name: "root differs from the declaration", mutate: func(c *partRestoreCall) { c.root = "00" + c.root[2:] }, fragment: "target event does not match"},
		{name: "source row hash differs", mutate: func(c *partRestoreCall) { c.sourceRowHash = []byte("different") }, fragment: "source event does not match"},
		{name: "source seq differs", mutate: func(c *partRestoreCall) { c.sourceSeq++ }, fragment: "source event does not match"},
		{name: "source shard differs", mutate: func(c *partRestoreCall) { c.sourceShard++ }, fragment: "source event does not match"},
		{name: "source does not exist", mutate: func(c *partRestoreCall) { c.source = uuid.Must(uuid.NewV7()) }, fragment: "source event does not match"},
		{
			name: "target already has a part",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(), readTestSQL(t, "part_restore_part_insert.sql"),
					fixture.org, fixture.target, fixture.call.root, fixture.payload); err != nil {
					t.Fatalf("insert a part for the target: %v", err)
				}
			},
			fragment: "already has",
		},
		{
			name: "source part was deleted",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(), readTestSQL(t, "manifest_test_delete_part.sql"), fixture.org, fixture.source); err != nil {
					t.Fatalf("delete the source part: %v", err)
				}
			},
			fragment: "does not store part 0",
		},
		{
			name: "source payload was altered",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(), `UPDATE audit.event_parts SET payload = $3 WHERE org_id = $1 AND event_id = $2`,
					fixture.org, fixture.source, []byte("altered payload")); err != nil {
					t.Fatalf("alter the source payload: %v", err)
				}
			},
			fragment: "does not hash to the declared root",
		},
		{
			name: "source digest differs",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(), `UPDATE audit.event_parts SET digest = $3 WHERE org_id = $1 AND event_id = $2`,
					fixture.org, fixture.source, "00"+fixture.call.root[2:]); err != nil {
					t.Fatalf("alter the source digest: %v", err)
				}
			},
			fragment: "does not hash to the declared root",
		},
		{
			name: "source declares another root",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(),
					`UPDATE audit.events SET context = jsonb_set(context, '{read,manifest_sha256}', to_jsonb($3::text)) WHERE org_id = $1 AND event_id = $2`,
					fixture.org, fixture.source, "00"+fixture.call.root[2:]); err != nil {
					t.Fatalf("alter the source root: %v", err)
				}
			},
			fragment: "source event does not match",
		},
		{
			name: "source declares two parts",
			prepare: func(t *testing.T, fixture partRestoreRawFixture) {
				if _, err := fixture.admin.Exec(t.Context(),
					`UPDATE audit.events SET context = jsonb_set(context, '{read,manifest_parts}', '2'::jsonb) WHERE org_id = $1 AND event_id = $2`,
					fixture.org, fixture.source); err != nil {
					t.Fatalf("alter the source part count: %v", err)
				}
			},
			fragment: "source event does not match",
		},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			fixture := newPartRestoreRawFixture(t, admin)
			if current.prepare != nil {
				current.prepare(t, fixture)
			}
			call := fixture.call
			if current.mutate != nil {
				current.mutate(&call)
			}
			before := readPartRestoreCounts(t, admin, call.changeID)
			inserted, err := call.run(t, operator)
			requirePartRestoreRefusal(t, err, current.fragment)
			if inserted != 0 {
				t.Fatalf("a refused call returned %d, want 0", inserted)
			}
			if after := readPartRestoreCounts(t, admin, call.changeID); after != before {
				t.Fatalf("counts after a refusal = %+v, want %+v", after, before)
			}
		})
	}
}

func TestAuditPartRestoreDuplicateChangeEventRollsBackPart(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	fixture := newPartRestoreRawFixture(t, admin)
	if _, err := admin.Exec(t.Context(), `INSERT INTO public.ops_outbox (event_id, event) VALUES ($1, '{"synthetic":"duplicate"}'::jsonb)`, fixture.call.changeID); err != nil {
		t.Fatalf("insert the conflicting outbox row: %v", err)
	}
	before := readPartRestoreCounts(t, admin, fixture.call.changeID)
	_, err := fixture.call.run(t, operator)
	requirePGCode(t, err, "23505")
	if after := readPartRestoreCounts(t, admin, fixture.call.changeID); after != before {
		t.Fatalf("counts after the duplicate error = %+v, want %+v (no part and no second outbox row)", after, before)
	}
}
