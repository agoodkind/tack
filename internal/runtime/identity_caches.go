package runtime

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
)

func buildIdentityCaches(
	cfg *config.Config,
	pool *pgxpool.Pool,
	orgMembers *auth.CachedMembers,
) (*auth.CachedTokenValidator, *auth.CachedUsers) {
	tokenRepo := auth.NewCachedTokenValidator(
		postgres.NewTokenRepo(pool),
		cfg.AuthTokenCacheLifetime,
		cfg.AuthTokenCacheSize,
	)
	// Cache user records that tool output reads for node attribution.
	userRepo := auth.NewCachedUsers(
		postgres.NewUserRepo(pool),
		cfg.AuthUserCacheLifetime,
		cfg.AuthUserCacheSize,
	)
	// Read the token and its user's organization set with one query on a cache miss.
	tokenRepo.PrimeMembers(orgMembers)

	return tokenRepo, userRepo
}
