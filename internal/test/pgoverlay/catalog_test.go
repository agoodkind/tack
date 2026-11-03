//go:build pgoverlay

package pgoverlay_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const serializationFailureCode = "40001"

const (
	renamePollInterval = 2 * time.Millisecond
	quietPollInterval  = 200 * time.Millisecond
	quietTimeout       = 30 * time.Second
)

// withAdmin runs fn with an administrator connection to the engine.
func (target databaseTarget) withAdmin(t *testing.T, fn func(ctx context.Context, connection *pgx.Conn)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), databaseTimeout)
	defer cancel()
	connection, err := pgx.Connect(ctx, target.connectionString(target.adminUser, target.adminPassword))
	if err != nil {
		t.Fatalf("connect as the administrator: %v", err)
	}
	defer func() { _ = connection.Close(ctx) }()
	fn(ctx, connection)
}

// execute runs one statement as the administrator.
func (target databaseTarget) execute(t *testing.T, statement string) {
	t.Helper()
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		if _, err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("run %q: %v", statement, err)
		}
	})
}

// roleOID returns the OID of the role with this name.
func (target databaseTarget) roleOID(t *testing.T, role string) int64 {
	t.Helper()
	var oid int64
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		query := "SELECT oid::bigint FROM pg_roles WHERE rolname = $1"
		if err := connection.QueryRow(ctx, query, role).Scan(&oid); err != nil {
			t.Fatalf("read the OID of role %s: %v", role, err)
		}
	})
	return oid
}

// roleNameByOID returns the current name of the role with this OID.
func (target databaseTarget) roleNameByOID(t *testing.T, oid int64) (string, bool) {
	t.Helper()
	var name string
	found := false
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		rows, err := connection.Query(ctx, "SELECT rolname FROM pg_roles WHERE oid = $1", oid)
		if err != nil {
			t.Fatalf("read the name of role OID %d: %v", oid, err)
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("read the name of role OID %d: %v", oid, err)
			}
			found = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the name of role OID %d: %v", oid, err)
		}
	})
	return name, found
}

// roleCount returns how many of these role names exist.
func (target databaseTarget) roleCount(t *testing.T, roles ...string) int {
	t.Helper()
	var count int
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		query := "SELECT count(*) FROM pg_roles WHERE rolname = ANY($1)"
		if err := connection.QueryRow(ctx, query, roles).Scan(&count); err != nil {
			t.Fatalf("count roles: %v", err)
		}
	})
	return count
}

// isMember reports whether member has a membership in parent.
func (target databaseTarget) isMember(t *testing.T, member, parent string) bool {
	t.Helper()
	var found bool
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		query := `SELECT EXISTS (SELECT 1 FROM pg_auth_members
			WHERE member = (SELECT oid FROM pg_roles WHERE rolname = $1)
			AND roleid = (SELECT oid FROM pg_roles WHERE rolname = $2))`
		if err := connection.QueryRow(ctx, query, member, parent).Scan(&found); err != nil {
			t.Fatalf("check membership of %s in %s: %v", member, parent, err)
		}
	})
	return found
}

// createRole creates a role. A non-empty password makes it a login role.
func (target databaseTarget) createRole(t *testing.T, role, password string) {
	t.Helper()
	statement := "CREATE ROLE " + pgx.Identifier{role}.Sanitize()
	if password != "" {
		literal := strings.ReplaceAll(password, "'", "''")
		statement += " LOGIN PASSWORD '" + literal + "'"
	}
	target.execute(t, statement)
}

// grantMembership makes member a member of parent.
func (target databaseTarget) grantMembership(t *testing.T, parent, member string) {
	t.Helper()
	target.execute(t, "GRANT "+pgx.Identifier{parent}.Sanitize()+" TO "+pgx.Identifier{member}.Sanitize())
}

// waitForRoleName polls until the role with this OID has the wanted name and
// fails the test when it does not within the timeout.
func (target databaseTarget) waitForRoleName(t *testing.T, oid int64, want string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	connection, err := pgx.Connect(ctx, target.connectionString(target.adminUser, target.adminPassword))
	if err != nil {
		t.Fatalf("connect to watch role OID %d: %v", oid, err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	for ctx.Err() == nil {
		var name string
		query := "SELECT rolname FROM pg_roles WHERE oid = $1"
		err := connection.QueryRow(ctx, query, oid).Scan(&name)
		if err == nil && name == want {
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && !isCatalogRetry(err) && ctx.Err() == nil {
			t.Fatalf("watch role OID %d: %v", oid, err)
		}
		time.Sleep(renamePollInterval)
	}
	t.Fatalf("role OID %d did not get the name %s within %s", oid, want, timeout)
}

// isCatalogRetry reports whether the engine asks the client to retry because a
// concurrent schema change invalidated the catalog snapshot (SQLSTATE 40001).
func isCatalogRetry(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == serializationFailureCode
}

// waitForQuietStatements polls until no other session runs a statement that
// mentions one of these role names.
func (target databaseTarget) waitForQuietStatements(t *testing.T, roles ...string) {
	t.Helper()
	deadline := time.Now().Add(quietTimeout)
	for time.Now().Before(deadline) {
		if target.activeStatements(t, roles...) == 0 {
			return
		}
		time.Sleep(quietPollInterval)
	}
	t.Fatalf("sessions still run statements for roles %v after %s", roles, quietTimeout)
}

func (target databaseTarget) activeStatements(t *testing.T, roles ...string) int {
	t.Helper()
	total := 0
	target.withAdmin(t, func(ctx context.Context, connection *pgx.Conn) {
		query := `SELECT count(*) FROM pg_stat_activity
			WHERE pid <> pg_backend_pid() AND state = 'active' AND query LIKE '%' || $1 || '%'`
		for _, role := range roles {
			var count int
			if err := connection.QueryRow(ctx, query, role).Scan(&count); err != nil {
				t.Fatalf("read session activity: %v", err)
			}
			total += count
		}
	})
	return total
}
