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

// CachedTokenValidator remembers accepted tokens for a short lifetime, so a
// request whose token was accepted moments ago costs no ledger read
// (TACK-504). Keys are the token's SHA-256, never the bearer. A stored record
// past its own expiry is refused whatever the cache lifetime says. A refusal
// is never cached, so a token created after a refusal is accepted at once.
// Revocation runs in another process, so it takes effect on every instance
// within one lifetime.
type CachedTokenValidator struct {
	inner    TokenValidator
	cache    *entryCache[*token.Token]
	lifetime time.Duration
	// members, when set and inner can answer the token and its org set in
	// one read, receives the org set, so the membership lookup that follows
	// in the same request is a cache hit (criterion 12: one ledger read on
	// a cold request).
	members *CachedMembers `exhaustruct:"optional"`
}

// TokenOrgsValidator answers a token and the org set of its holder in one
// ledger read.
type TokenOrgsValidator interface {
	ValidateWithOrgs(ctx context.Context, raw string) (*token.Token, []uuid.UUID, error)
}

// PrimeMembers makes every miss that inner can answer with the holder's org
// set store that set in members.
func (c *CachedTokenValidator) PrimeMembers(members *CachedMembers) {
	c.members = members
}

// NewCachedTokenValidator wraps inner. A lifetime of zero or less returns a
// wrapper that caches nothing.
func NewCachedTokenValidator(inner TokenValidator, lifetime time.Duration, size int) *CachedTokenValidator {
	if size <= 0 {
		size = 1
	}
	return &CachedTokenValidator{inner: inner, cache: newEntryCache[*token.Token](size), lifetime: lifetime}
}

// Validate answers from the cache when it can, and asks inner otherwise.
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

// lookup asks inner for the token, in the same read as the holder's org set
// when both inner and members allow it.
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

// tokenCacheKey is the full SHA-256 of the bearer, the same identity the
// token table stores, so the cache never holds a bearer.
func tokenCacheKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
