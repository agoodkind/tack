package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/testenv"
)

// sessionLockWait is the time a test waits after it locks a row and before it
// commits the lock transaction.
const sessionLockWait = 3 * time.Second

// sessionRoleFixture contains a ledger administrator pool, an application pool
// with one connection, and a token that the application login validates.
type sessionRoleFixture struct {
	admin *pgxpool.Pool
	// application has a limit of one connection. In production, one
	// connection ran both the read lifecycle calls and token validation.
	application *pgxpool.Pool
	store       audit.ReadLifecycle
	login       string
	rawToken    string
}

// newSessionRoleFixture creates a login that inherits app_auth, opens a
// one-connection pool for it, and stores a token for a new user.
func newSessionRoleFixture(t *testing.T) sessionRoleFixture {
	t.Helper()
	dsn := testenv.Ledger(t)
	admin := ledgerAdminPool(t)
	login, loginDSN := applicationLogin(t, admin, dsn)
	config, err := pgxpool.ParseConfig(loginDSN)
	if err != nil {
		t.Fatalf("parse the application login address: %v", err)
	}
	config.MaxConns = 1
	application, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open the application pool: %v", err)
	}
	t.Cleanup(application.Close)
	ctx, _ := sqlAuditContext(t, sqlAuditPrincipal())
	now := time.Now().UTC()
	holder, err := postgres.NewUserRepo(admin).Create(ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "session-role-" + uuid.NewString() + "@example.invalid",
		DisplayName: "Session Role Holder", AvatarURL: nil, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create the token holder: %v", err)
	}
	rawToken := "tack_" + uuid.NewString()
	if _, err := postgres.NewTokenRepo(admin).Create(ctx, uuid.Must(uuid.NewV7()), holder.ID, rawToken, "session role"); err != nil {
		t.Fatalf("create the token: %v", err)
	}
	return sessionRoleFixture{
		admin: admin, application: application, store: audit.NewPoolReadLifecycle(application),
		login: login, rawToken: rawToken,
	}
}

// applicationLogin creates a login that inherits app_auth, as tack_app does,
// and returns its name and a DSN that connects as it.
func applicationLogin(t *testing.T, admin *pgxpool.Pool, adminDSN string) (string, string) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate the login secret: %v", err)
	}
	login := "tack_test_session_" + hex.EncodeToString(secret[:4])
	password := hex.EncodeToString(secret)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create login %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+login) })
	if _, err := admin.Exec(ctx, "GRANT app_auth TO "+login); err != nil {
		t.Fatalf("grant app_auth to %s: %v", login, err)
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the ledger address: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	return login, parsed.String()
}

// lockReadState opens an administrator transaction that writes the
// ops_read_state row of eventID. The row stays locked until the caller
// commits the returned transaction.
func lockReadState(t *testing.T, admin *pgxpool.Pool, eventID uuid.UUID) pgx.Tx {
	t.Helper()
	transaction, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the lock transaction: %v", err)
	}
	t.Cleanup(func() { _ = transaction.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := transaction.Exec(t.Context(), readTestSQL(t, "runtime_audit_read_session_lock.sql"), eventID); err != nil {
		t.Fatalf("lock read state %s: %v", eventID, err)
	}
	return transaction
}

// requireApplicationSession reads current_user and session_user on the one
// application connection and validates the fixture token on that connection
// with TokenRepo.ValidateWithOrgs, the query that the bearer middleware runs.
// Both roles must equal the login.
func (fixture sessionRoleFixture) requireApplicationSession(t *testing.T, step string) {
	t.Helper()
	var currentUser, sessionUser string
	roleQuery := readTestSQL(t, "runtime_audit_read_session_role.sql")
	if err := fixture.application.QueryRow(t.Context(), roleQuery).Scan(&currentUser, &sessionUser); err != nil {
		t.Fatalf("%s: read the application session role: %v", step, err)
	}
	_, _, validateErr := postgres.NewTokenRepo(fixture.application).ValidateWithOrgs(t.Context(), fixture.rawToken)
	t.Logf("%s: current_user = %s, session_user = %s, token validation error = %v",
		step, currentUser, sessionUser, validateErr)
	if currentUser != fixture.login || sessionUser != fixture.login {
		t.Fatalf("%s: current_user = %s and session_user = %s, want %s for both", step, currentUser, sessionUser, fixture.login)
	}
	if validateErr != nil {
		t.Fatalf("%s: validate the token on the application connection: %v", step, validateErr)
	}
}
