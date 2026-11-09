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

// CachedMembers caches each user's organizations for a short lifetime. Cache
// hits do not read the database. Each instance reads current memberships on
// its next lookup after expiry. Failed reads do not populate the cache.
// Handlers after the middleware reject access if their membership read
// fails. CachedMembers does not provide write methods.
type CachedMembers struct {
	inner    org.MemberRepository
	cache    *entryCache[[]uuid.UUID]
	lifetime time.Duration
}

// NewCachedMembers wraps inner.
//
// A lifetime of zero or less disables caching. A size of zero or less
// selects an entry limit of one.
//
// The membership-cache tests configure a one-minute lifetime and 16 entries:
//
//	cached := NewCachedMembers(store, time.Minute, 16)
func NewCachedMembers(inner org.MemberRepository, lifetime time.Duration, size int) *CachedMembers {
	if size <= 0 {
		size = 1
	}

	return &CachedMembers{inner: inner, cache: newEntryCache[[]uuid.UUID](size), lifetime: lifetime}
}

// ListOrgIDsForUser returns cached organizations when the entry is valid.
//
// Otherwise, it reads inner. Callers receive a copy separate from the cache.
// A failed read does not populate the cache.
//
// The return values distinguish these outcomes:
//   - A successful read returns the user's organization IDs and a nil error.
//   - A failed read returns a nil slice and an error that includes userID.
func (c *CachedMembers) ListOrgIDsForUser(
	ctx context.Context,
	userID uuid.UUID,
) ([]uuid.UUID, error) {
	now := clock.Now()
	key := userID.String()
	if c.lifetime > 0 {
		if cached, ok := c.cache.get(key, now); ok {
			return copyOrgIDs(cached), nil
		}
	}

	orgIDs, err := c.inner.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"auth.membership_read_failed",
			slog.String("user_id", userID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("list org ids for user %s: %w", userID, err)
	}

	if c.lifetime > 0 {
		c.cache.put(key, copyOrgIDs(orgIDs), now.Add(c.lifetime))
	}

	return orgIDs, nil
}

// prime stores organizations from a combined token and membership read. The
// next membership lookup in that request can use this cached set.
func (c *CachedMembers) prime(userID uuid.UUID, orgIDs []uuid.UUID, now time.Time) {
	if c.lifetime <= 0 {
		return
	}

	c.cache.put(userID.String(), copyOrgIDs(orgIDs), now.Add(c.lifetime))
}

// ListMembers reads the underlying repository without caching organization
// rosters.
//
// A successful read returns the repository's roster. A failed read returns a
// nil slice and wraps the repository error with orgID using %w.
//
// Callers can use the organization ID from a resolved workspace:
//
//	roster, err := members.ListMembers(ctx, ws.OrgID)
func (c *CachedMembers) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*org.Member, error) {
	members, err := c.inner.ListMembers(ctx, orgID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"auth.members_list_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("list members of %s: %w", orgID, err)
	}

	return members, nil
}

func copyOrgIDs(orgIDs []uuid.UUID) []uuid.UUID {
	copied := make([]uuid.UUID, len(orgIDs))
	copy(copied, orgIDs)

	return copied
}
