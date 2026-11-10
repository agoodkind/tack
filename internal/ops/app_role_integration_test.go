package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/testenv"
)

// permissionDeniedSQLState identifies the engine's refusal when a role lacks
// permission for an object.
const permissionDeniedSQLState = "42501"

// TestAppRoleReachesTheAuthTablesAndNothingElse checks TACK-180 against the
// engine with a login that inherits only app_auth, the base role for tack_app.
// Real repositories create a user, membership, and token, list the token, and
// delete the token and membership. The test checks permission refusals on the
// audit ledger, operator outbox, and migration ledger. These checks detect
// grants that could let SQL injection through the application pool read or
// change compliance records.
func TestAppRoleReachesTheAuthTablesAndNothingElse(t *testing.T) {
	dsn := testenv.Ledger(t)
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
	ctx = actAsFixtureContext(t)
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
	minted, err := tokens.Create(ctx, orgID, created.ID, "tack_"+uuid.NewString(), "tack-180")
	if err != nil {
		t.Fatalf("create a token as the app login: %v", err)
	}
	if listed, err := tokens.List(ctx, created.ID); err != nil || len(listed) != 1 {
		t.Fatalf("list tokens as the app login: %d, %v", len(listed), err)
	}
	if err := tokens.Delete(ctx, orgID, minted.ID); err != nil {
		t.Fatalf("delete a token as the app login: %v", err)
	}
	if err := members.RemoveMember(ctx, orgID, created.ID); err != nil {
		t.Fatalf("remove a membership as the app login: %v", err)
	}

	// Check each protected table for SELECT, INSERT, UPDATE, and DELETE
	// permissions. A read refusal alone cannot detect an accidental mutation grant.
	// Each statement runs in a separate transaction, and the helper always rolls it
	// back to remove any probe changes.
	for _, table := range []string{
		"audit.events", "audit.notarizations", "public.ops_outbox", "public.goose_db_version",
	} {
		for _, refused := range []string{
			"SELECT count(*) FROM " + table,
			"INSERT INTO " + table + " DEFAULT VALUES",
			// Use DEFAULT without reading a column. An UPDATE that reads a column also
			// requires SELECT. A SELECT refusal could hide an accidental UPDATE grant.
			"UPDATE " + table + " SET " + firstColumn(ctx, t, admin, table) + " = DEFAULT WHERE false",
			"DELETE FROM " + table + " WHERE false",
		} {
			assertPermissionDenied(ctx, t, app, refused)
		}
	}
	// The public schema's default CREATE grant to PUBLIC would let the app
	// login create arbitrary objects beside the auth tables.
	assertPermissionDenied(ctx, t, app, "CREATE TABLE public.tack180_probe (id int)")
}

// assertPermissionDenied runs statement as the app login and requires SQLSTATE
// 42501, which indicates a permission refusal. It always rolls back the
// transaction.
func assertPermissionDenied(ctx context.Context, t *testing.T, app *pgxpool.Pool, statement string) {
	t.Helper()
	transaction, err := app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin as the app login: %v", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	_, err = transaction.Exec(ctx, statement)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != permissionDeniedSQLState {
		t.Fatalf("%q as the app login: err = %v, want permission denied", statement, err)
	}
}

// firstColumn reads a column name for table without requiring prior schema
// knowledge. The UPDATE probe checks permission without updating rows.
func firstColumn(ctx context.Context, t *testing.T, admin *pgxpool.Pool, table string) string {
	t.Helper()
	schema, name, _ := strings.Cut(table, ".")
	var column string
	if err := admin.QueryRow(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position LIMIT 1`,
		schema, name,
	).Scan(&column); err != nil {
		t.Fatalf("first column of %s: %v", table, err)
	}
	return column
}

// appAuthLoginDSN creates a temporary login that inherits only app_auth and
// returns a DSN that authenticates as that login.
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
