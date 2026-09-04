package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

// appRoleTestDSNEnv names a migrated ledger DSN with role-creation privilege.
const appRoleTestDSNEnv = "AUDIT_CHAIN_TEST_DSN"

// permissionDeniedSQLState is what the engine answers a role that holds no
// grant on the object it named.
const permissionDeniedSQLState = "42501"

// TestAppRoleReachesTheAuthTablesAndNothingElse is TACK-180's acceptance
// against the engine. A login holding only app_auth, the base role the
// application's tack_app inherits, does the application's own work through
// the real repositories: it creates a user, a membership, and a token, reads
// them back, and deletes them. The same login is refused on the ledger, on
// the operator outbox, and on the migration ledger, so an injection through
// the application pool cannot read or touch the compliance record.
func TestAppRoleReachesTheAuthTablesAndNothingElse(t *testing.T) {
	dsn := os.Getenv(appRoleTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated ledger DSN to run", appRoleTestDSNEnv)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	app, err := pgxpool.New(ctx, appAuthLoginDSN(ctx, t, admin, dsn))
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)

	users := postgres.NewUserRepo(app)
	members := postgres.NewOrgMemberRepo(app)
	tokens := postgres.NewTokenRepo(app)
	orgID := uuid.Must(uuid.NewV7())
	created, err := users.Create(ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "tack180-" + uuid.NewString()[:8] + "@example.test",
		DisplayName: "TACK-180", AvatarURL: nil,
	})
	if err != nil {
		t.Fatalf("create a user as the app login: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DELETE FROM users WHERE id = $1`, created.ID) })
	if err := members.AddMember(ctx, &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: created.ID, Role: 20}); err != nil {
		t.Fatalf("add a membership as the app login: %v", err)
	}
	minted, err := tokens.Create(ctx, created.ID, "tack_"+uuid.NewString(), "tack-180")
	if err != nil {
		t.Fatalf("create a token as the app login: %v", err)
	}
	if listed, err := tokens.List(ctx, created.ID); err != nil || len(listed) != 1 {
		t.Fatalf("list tokens as the app login: %d, %v", len(listed), err)
	}
	if err := tokens.Delete(ctx, minted.ID); err != nil {
		t.Fatalf("delete a token as the app login: %v", err)
	}
	if err := members.RemoveMember(ctx, orgID, created.ID); err != nil {
		t.Fatalf("remove a membership as the app login: %v", err)
	}

	for _, refused := range []string{
		`SELECT count(*) FROM audit.events`,
		`SELECT count(*) FROM audit.notarizations`,
		`SELECT count(*) FROM public.ops_outbox`,
		`SELECT count(*) FROM public.goose_db_version`,
	} {
		var n int64
		err := app.QueryRow(ctx, refused).Scan(&n)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDeniedSQLState {
			t.Fatalf("%q as the app login: err = %v, want permission denied", refused, err)
		}
	}
}

// appAuthLoginDSN creates a throwaway login that inherits app_auth and nothing
// else and returns the DSN rewritten to connect as it.
func appAuthLoginDSN(ctx context.Context, t *testing.T, admin *pgxpool.Pool, adminDSN string) string {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("login suffix: %v", err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("login secret: %v", err)
	}
	login := "tack_test_app_" + hex.EncodeToString(suffix)
	encodedSecret := hex.EncodeToString(secret)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '"+encodedSecret+"'"); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+login) })
	if _, err := admin.Exec(ctx, "GRANT app_auth TO "+login); err != nil {
		t.Fatalf("grant app_auth to %s: %v", login, err)
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, encodedSecret)
	return parsed.String()
}
