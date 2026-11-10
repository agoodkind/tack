//go:build integration

package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"expvar"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/migrations"
)

// latestMigrationVersion returns the highest goose version among the SQL files
// in migrations.FS, read from each file name's numeric prefix.
func latestMigrationVersion(t *testing.T) int64 {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	var latest int64
	for _, entry := range entries {
		prefix, _, found := strings.Cut(entry.Name(), "_")
		if !found || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("parse migration version from %s: %v", entry.Name(), err)
		}
		latest = max(latest, version)
	}
	if latest == 0 {
		t.Fatal("migrations.FS has no versioned SQL file")
	}
	return latest
}

// auditWriterPool opens a pool on the fixture database as a throwaway LOGIN
// role that inherits only audit_writer.
func auditWriterPool(t *testing.T, fixture *partmanTestDatabase) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	suffix := make([]byte, 4)
	secret := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("role suffix: %v", err)
	}
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("role secret: %v", err)
	}
	login := "tack_test_partition_writer_" + hex.EncodeToString(suffix)
	password := hex.EncodeToString(secret)
	identifier := pgx.Identifier{login}.Sanitize()
	if _, err := fixture.adminPool.Exec(ctx, "CREATE ROLE "+identifier+" LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	if _, err := fixture.adminPool.Exec(ctx, "GRANT audit_writer TO "+identifier); err != nil {
		t.Fatalf("grant audit_writer to %s: %v", login, err)
	}
	parsed, err := url.Parse(fixture.dsn)
	if err != nil {
		t.Fatalf("parse fixture DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open %s pool: %v", login, err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := fixture.adminPool.Exec(context.Background(), "DROP ROLE IF EXISTS "+identifier); err != nil {
			t.Errorf("drop %s: %v", login, err)
		}
	})
	return pool
}

// maintenanceOutcomes reads one entry of the exported maintenance counter; an
// absent entry is zero.
func maintenanceOutcomes(t *testing.T, outcome string) int64 {
	t.Helper()
	counter, ok := expvar.Get("tack_audit_partition_maintenance_total").(*expvar.Map)
	if !ok {
		t.Fatalf("expvar tack_audit_partition_maintenance_total is not registered as a map")
	}
	value, ok := counter.Get(outcome).(*expvar.Int)
	if !ok {
		return 0
	}
	return value.Value()
}
