//go:build integration

package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"expvar"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
