package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

// CachedTokenValidator caches accepted tokens for a short lifetime. A cache
// hit avoids a database read. Cache keys use the token's SHA-256 hash and do
// not store the bearer value. The validator rejects a cached token at its
// own expiry even if the cache entry has not expired. It does not cache
// refusals. A token created after a refusal can pass the next lookup.
// Revocation in another process takes effect at each instance's next lookup
// after the cache entry expires.
type CachedTokenValidator struct {
	inner    TokenValidator
	cache    *entryCache[*token.Token]
	lifetime time.Duration
	// members stores the organization set from a combined token and membership
	// read when inner supports it. With a positive membership cache lifetime,
	// the next membership lookup in the request uses this set. A cold request
	// then needs one database read for token and membership validation.
	members *CachedMembers `exhaustruct:"optional"`
}

// TokenOrgsValidator returns a token and its user's organizations from one
// database read.
type TokenOrgsValidator interface {
	ValidateWithOrgs(ctx context.Context, raw string) (*token.Token, []uuid.UUID, error)
}

// PrimeMembers selects the membership cache for successful token lookups
// that also return the user's organizations.
//
// A lookup can populate this cache when inner implements TokenOrgsValidator.
// The membership cache needs a positive lifetime to retain the returned set.
//
// The runtime configures this cache before constructing the auth middleware:
//
//	tokenRepo.PrimeMembers(orgMembers)
func (c *CachedTokenValidator) PrimeMembers(members *CachedMembers) {
	c.members = members
}

// NewCachedTokenValidator wraps inner.
//
// A lifetime of zero or less disables token caching. A size of zero or less
// selects an entry limit of one.
//
// The token-cache tests configure a one-minute lifetime and 16 entries:
//
//	cached := NewCachedTokenValidator(store, time.Minute, 16)
func NewCachedTokenValidator(
	inner TokenValidator,
	lifetime time.Duration,
	size int,
) *CachedTokenValidator {
	if size <= 0 {
		size = 1
	}

	return &CachedTokenValidator{
		inner:    inner,
		cache:    newEntryCache[*token.Token](size),
		lifetime: lifetime,
	}
}

// Validate checks whether raw is an accepted bearer token.
//
// Cache entries expire independently of the tokens they contain:
//   - A valid cache entry with an unexpired token returns a copy of the
//     token.
//   - A valid cache entry with an expired token returns
//     domain.ErrUnauthenticated without calling inner.
//   - An absent or expired cache entry requires a lookup through inner.
func (c *CachedTokenValidator) Validate(ctx context.Context, raw string) (*token.Token, error) {
	now := clock.Now()
	key := tokenCacheKey(raw)
	if c.lifetime > 0 {
		if cached, ok := c.cache.get(key, now); ok {
			if cached.ExpiresAt != nil && !cached.ExpiresAt.After(now) {
				c.cache.remove(key)
				return nil, domain.ErrUnauthenticated
			}

			record := *cached

			return &record, nil
		}
	}

	record, err := c.lookup(ctx, raw, now)
	if err != nil {
		return nil, err
	}

	if c.lifetime > 0 {
		stored := *record
		c.cache.put(key, &stored, now.Add(c.lifetime))
	}

	return record, nil
}

// lookup asks inner for the token. It reads the user's organizations in the
// same call when inner implements TokenOrgsValidator and members has a
// cache.
func (c *CachedTokenValidator) lookup(ctx context.Context, raw string, now time.Time) (*token.Token, error) {
	withOrgs, ok := c.inner.(TokenOrgsValidator)
	if !ok || c.members == nil {
		record, err := c.inner.Validate(ctx, raw)
		if isUnauthenticated(err) {
			return nil, domain.ErrUnauthenticated
		}

		if err != nil {
			slog.ErrorContext(ctx, "auth.token_validate_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("validate token: %w", err)
		}

		return record, nil
	}

	record, orgIDs, err := withOrgs.ValidateWithOrgs(ctx, raw)
	if isUnauthenticated(err) {
		return nil, domain.ErrUnauthenticated
	}

	if err != nil {
		slog.ErrorContext(ctx, "auth.token_validate_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("validate token with orgs: %w", err)
	}

	c.members.prime(record.UserID, orgIDs, now)

	return record, nil
}

// tokenCacheKey returns the bearer's full SHA-256 hash, matching the token
// table's identity. The cache does not store bearer values.
func tokenCacheKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
