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

// CachedTokenValidator caches accepted tokens for a limited lifetime to
// avoid repeated ledger reads (TACK-504). Keys contain SHA-256 hashes rather
// than bearers. Validation rejects expired records even during the cache
// lifetime. The cache does not store refusals. Each instance applies remote
// revocations within one cache lifetime.
type CachedTokenValidator struct {
	inner    TokenValidator
	cache    *entryCache[*token.Token]
	lifetime time.Duration
	// members caches the org set when inner supports ValidateWithOrgs.
	// The following membership lookup can use this set without another
	// ledger read (criterion 12).
	members *CachedMembers `exhaustruct:"optional"`
}

// TokenOrgsValidator reads a token and its user's org memberships in one
// ledger query.
type TokenOrgsValidator interface {
	ValidateWithOrgs(ctx context.Context, raw string) (*token.Token, []uuid.UUID, error)
}

// PrimeMembers enables membership caching when inner supports ValidateWithOrgs.
func (c *CachedTokenValidator) PrimeMembers(members *CachedMembers) {
	c.members = members
}

// NewCachedTokenValidator wraps inner. A lifetime of zero or less disables
// caching.
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

// Validate returns a cached record when valid and queries inner otherwise.
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

// lookup queries inner for the token. It also reads the user's org set
// when inner supports ValidateWithOrgs and members enables membership caching.
func (c *CachedTokenValidator) lookup(
	ctx context.Context,
	raw string,
	now time.Time,
) (*token.Token, error) {
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

// tokenCacheKey hashes the bearer with SHA-256. The cache and token table
// identify tokens by this hash rather than storing the bearer.
func tokenCacheKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
