package servercli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
)

// userIDNamespace separates user IDs based on email from node IDs.
// Development bearer authentication uses the same user ID after each
// database reset.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID from a lowercased email.
// Email case differences do not change the seed user's identity.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// generateToken returns a fresh random bearer token in the format every
// issuer uses.
func generateToken() (string, error) {
	raw, err := auth.NewBearerToken()
	if err != nil {
		slog.Error("seed.token_failed", slog.String("err", err.Error()))

		return "", fmt.Errorf("seed: generate token: %w", err)
	}

	return raw, nil
}

// orgsExist checks SQL membership records because FoundationDB stores
// the organization nodes. It returns false if migrations have not
// created org_members.
func orgsExist(ctx context.Context, cfg *config.Config) (bool, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "seed.orgs_exist_open_failed", slog.String("err", err.Error()))

		return false, fmt.Errorf("orgsExist: open db: %w", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM org_members LIMIT 1)`).Scan(&exists)
	if err != nil {
		// Treat an absent pre-migration table as empty.
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}

		slog.ErrorContext(ctx, "seed.orgs_exist_query_failed", slog.String("err", err.Error()))

		return false, fmt.Errorf("orgsExist: query: %w", err)
	}

	return exists, nil
}
