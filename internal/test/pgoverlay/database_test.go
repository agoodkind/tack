//go:build pgoverlay

package pgoverlay_test

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const databaseTimeout = 30 * time.Second

// databaseTarget is one local database engine and its superuser login.
type databaseTarget struct {
	host          string
	port          string
	database      string
	adminUser     string
	adminPassword string
	// expectedVersion is the provider expected_version setting. The provider
	// detects the server version when the value is the provider default.
	expectedVersion string
}

func parseTarget(t *testing.T, dsn, expectedVersion string) databaseTarget {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the engine connection string: %v", err)
	}
	password, _ := parsed.User.Password()
	return databaseTarget{
		host:            parsed.Hostname(),
		port:            parsed.Port(),
		database:        parsed.Path[1:],
		adminUser:       parsed.User.Username(),
		adminPassword:   password,
		expectedVersion: expectedVersion,
	}
}

// roleExists reports whether the engine has a role with this name.
func (target databaseTarget) roleExists(t *testing.T, role string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), databaseTimeout)
	defer cancel()
	connection, err := pgx.Connect(ctx, target.connectionString(target.adminUser, target.adminPassword))
	if err != nil {
		t.Fatalf("connect to check role %s: %v", role, err)
	}
	defer func() { _ = connection.Close(ctx) }()
	var found bool
	query := "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)"
	if err := connection.QueryRow(ctx, query, role).Scan(&found); err != nil {
		t.Fatalf("check role %s: %v", role, err)
	}
	return found
}

func (target databaseTarget) connectionString(user, password string) string {
	login := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(target.host, target.port),
		Path:     "/" + target.database,
		RawQuery: "sslmode=disable",
	}
	return login.String()
}

// loginError returns the error from a login as role, or nil when the login works.
func (target databaseTarget) loginError(t *testing.T, role, password string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), databaseTimeout)
	defer cancel()
	connection, err := pgx.Connect(ctx, target.connectionString(role, password))
	if err != nil {
		return err
	}
	return connection.Close(ctx)
}

// dropRole removes a role the test created. The provider cannot drop it on
// YugabyteDB because its delete also takes an advisory lock.
func (target databaseTarget) dropRole(t *testing.T, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), databaseTimeout)
	defer cancel()
	connection, err := pgx.Connect(ctx, target.connectionString(target.adminUser, target.adminPassword))
	if err != nil {
		t.Errorf("connect to remove role %s: %v", role, err)
		return
	}
	defer func() { _ = connection.Close(ctx) }()
	if _, err := connection.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Errorf("remove role %s: %v", role, err)
	}
}
