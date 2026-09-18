package datagen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
)

// expiredTokenTestDSNEnv names a migrated ledger DSN with role-creation
// privilege, the same one the app-role acceptance test reads.
const expiredTokenTestDSNEnv = "AUDIT_CHAIN_TEST_DSN"

// TestExpireTokenRunsAsTheAppLogin pins the QA failure of 2026-09-18: after
// migration 015 took UPDATE on api_tokens away from app_auth, the generator's
// expired-token step failed with permission denied and every soak stopped at
// bootstrap. The step now runs as a login holding only app_auth, stores a
// token already past its expiry, and the real validator refuses it.
func TestExpireTokenRunsAsTheAppLogin(t *testing.T) {
	dsn := os.Getenv(expiredTokenTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated ledger DSN to run", expiredTokenTestDSNEnv)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	app, err := pgxpool.New(ctx, appLoginDSN(ctx, t, admin, dsn))
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)

	holder, err := postgres.NewUserRepo(app).Create(ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "expired-" + uuid.NewString()[:8] + "@example.test",
		DisplayName: "expired token holder", AvatarURL: nil,
	})
	if err != nil {
		t.Fatalf("create the holder as the app login: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DELETE FROM users WHERE id = $1`, holder.ID) })

	raw, err := tokenFor()
	if err != nil {
		t.Fatalf("generate a token: %v", err)
	}
	actor := Actor{UserID: holder.ID, Email: holder.Email, DisplayName: "", Token: "", RawToken: "", RequestToken: ""}
	if err := expireToken(ctx, app, raw, actor); err != nil {
		t.Fatalf("expireToken as the app login: %v", err)
	}

	if _, err := postgres.NewTokenRepo(app).Validate(ctx, raw); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("Validate(expired) err = %v, want unauthenticated", err)
	}
	stored, err := postgres.NewTokenRepo(app).List(ctx, holder.ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("list the holder's tokens: %d, %v", len(stored), err)
	}
	if stored[0].ExpiresAt == nil || !stored[0].ExpiresAt.Before(clock.Now()) {
		t.Fatalf("expires_at = %v, want a time in the past", stored[0].ExpiresAt)
	}
}

// appLoginDSN creates a throwaway login that inherits app_auth and nothing
// else and returns the DSN rewritten to connect as it.
func appLoginDSN(ctx context.Context, t *testing.T, admin *pgxpool.Pool, adminDSN string) string {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("login suffix: %v", err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("login secret: %v", err)
	}
	login := "tack_test_datagen_" + hex.EncodeToString(suffix)
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
