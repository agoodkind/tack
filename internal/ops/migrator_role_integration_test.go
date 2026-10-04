package ops

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const notOwnedByMigratorQuery = `
	SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	         WHERE n.nspname IN ('audit', 'partman', 'public') AND c.relkind IN ('r', 'p', 'v', 'S')
	           AND pg_get_userbyid(c.relowner) <> 'tack_migrator')
	     + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	         WHERE n.nspname IN ('audit', 'partman', 'public') AND p.prokind IN ('f', 'p')
	           AND pg_get_userbyid(p.proowner) <> 'tack_migrator')
	     + (SELECT count(*) FROM pg_namespace
	         WHERE nspname IN ('audit', 'partman', 'public')
	           AND pg_get_userbyid(nspowner) <> 'tack_migrator')`

const eventPartitionCountQuery = `
	SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
	  JOIN pg_namespace n ON n.oid = p.relnamespace
	 WHERE n.nspname = 'audit' AND p.relname = 'events'`

// TestMigratorLoginDoesTheSuperuserWork runs seed-roles once as the engine
// superuser, as a first boot does, and every later step as tack_migrator
// (TACK-554).
func TestMigratorLoginDoesTheSuperuserWork(t *testing.T) {
	adminDSN := testenv.Ledger(t)
	ctx := t.Context()
	admin := testenv.LedgerPool(t, adminDSN)
	cfg := &config.Config{DatabaseURL: adminDSN}
	for _, generated := range []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	} {
		*generated = uuid.NewString()
	}
	if err := RunAuditSeedRoles(ctx, cfg); err != nil {
		t.Fatalf("seed-roles as the engine superuser: %v", err)
	}

	migratorDSN := loginDSN(t, adminDSN, migratorLogin, cfg.MigratorPassword)
	migrator := testenv.LedgerPool(t, migratorDSN)
	var superuser bool
	if err := migrator.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&superuser); err != nil || superuser {
		t.Fatalf("tack_migrator superuser = %v, err = %v; want a login that is not a superuser", superuser, err)
	}

	if err := postgres.Migrate(ctx, migratorDSN, migrations.FS); err != nil {
		t.Fatalf("migrate as tack_migrator: %v", err)
	}
	for _, statement := range []string{
		"CREATE TABLE audit.tack554_owner_probe (id int)",
		"GRANT SELECT ON audit.tack554_owner_probe TO audit_reader",
		"DROP TABLE audit.tack554_owner_probe",
	} {
		if _, err := migrator.Exec(ctx, statement); err != nil {
			t.Fatalf("%q as tack_migrator: %v", statement, err)
		}
	}

	asMigrator := *cfg
	asMigrator.DatabaseURL = migratorDSN
	if err := RunAuditSeedRoles(ctx, &asMigrator); err != nil {
		t.Fatalf("seed-roles as tack_migrator: %v", err)
	}

	before := countRows(ctx, t, admin, eventPartitionCountQuery)
	if _, err := migrator.Exec(ctx,
		`UPDATE partman.part_config SET premake = premake + 1 WHERE parent_table = 'audit.events'`); err != nil {
		t.Fatalf("raise the pg_partman premake as tack_migrator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx),
			`UPDATE partman.part_config SET premake = premake - 1 WHERE parent_table = 'audit.events'`)
	})
	writer := testenv.LedgerPool(t, loginDSN(t, adminDSN, "tack_audit_writer", cfg.AuditWriterPassword))
	if _, err := writer.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("partition maintenance as the audit writer: %v", err)
	}
	if after := countRows(ctx, t, admin, eventPartitionCountQuery); after <= before {
		t.Fatalf("partitions of audit.events = %d after maintenance, want more than %d", after, before)
	}
	if stray := countRows(ctx, t, admin, notOwnedByMigratorQuery); stray != 0 {
		t.Fatalf("%d objects in audit, partman, and public are not owned by tack_migrator", stray)
	}

	if _, err := migrator.Exec(ctx, `SELECT count(*) FROM audit.events`); err != nil {
		t.Fatalf("read the ledger as tack_migrator: %v", err)
	}
	_, err := migrator.Exec(ctx, `INSERT INTO audit.chain_heads (org_id, shard, last_seq, last_hash, updated_at)
		VALUES ('019ff315-bc5d-7a56-b12a-1a35f280c4dd', 0, 1, '\x00', now())`)
	var refused *pgconn.PgError
	if !errors.As(err, &refused) || refused.Code != permissionDeniedSQLState {
		t.Fatalf("ledger insert as tack_migrator: err = %v, want the row-level security refusal", err)
	}
}

func loginDSN(t *testing.T, adminDSN, login, secret string) string {
	t.Helper()
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, secret)
	return parsed.String()
}

func countRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}
