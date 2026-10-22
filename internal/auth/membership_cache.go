package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/org"
)

// CachedMembers caches each user's org set for a short lifetime. Cached
// membership lookups do not read the ledger, and every instance observes
// membership changes within one lifetime. Failed reads do not populate the
// cache; checks behind the middleware still reject access on read errors.
// CachedMembers does not provide write methods.
type CachedMembers struct {
	inner    org.MemberRepository
	cache    *entryCache[[]uuid.UUID]
	lifetime time.Duration
}

// NewCachedMembers wraps inner. A lifetime of zero or less returns a wrapper
// that caches nothing.
func NewCachedMembers(inner org.MemberRepository, lifetime time.Duration, size int) *CachedMembers {
	if size <= 0 {
		size = 1
	}
	return &CachedMembers{inner: inner, cache: newEntryCache[[]uuid.UUID](size), lifetime: lifetime}
}

// ListOrgIDsForUser answers from the cache when it can, and asks inner
// otherwise. Callers get their own copy of the set.
func (c *CachedMembers) ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	now := clock.Now()
	key := userID.String()
	if c.lifetime > 0 {
		if cached, ok := c.cache.get(key, now); ok {
			return copyOrgIDs(cached), nil
		}
	}
	orgIDs, err := c.inner.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.membership_read_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("list org ids for user %s: %w", userID, err)
	}
	if c.lifetime > 0 {
		c.cache.put(key, copyOrgIDs(orgIDs), now.Add(c.lifetime))
	}
	return orgIDs, nil
}

// prime stores an org set read by another lookup in the same ledger read,
// so the request that made that read finds the set here.
func (c *CachedMembers) prime(userID uuid.UUID, orgIDs []uuid.UUID, now time.Time) {
	if c.lifetime <= 0 {
		return
	}
	c.cache.put(userID.String(), copyOrgIDs(orgIDs), now.Add(c.lifetime))
}

// ListMembers reads through inner; org rosters are not cached.
func (c *CachedMembers) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*org.Member, error) {
	members, err := c.inner.ListMembers(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.members_list_failed",
			slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("list members of %s: %w", orgID, err)
	}
	return members, nil
}

func copyOrgIDs(orgIDs []uuid.UUID) []uuid.UUID {
	copied := make([]uuid.UUID, len(orgIDs))
	copy(copied, orgIDs)
	return copied
}
