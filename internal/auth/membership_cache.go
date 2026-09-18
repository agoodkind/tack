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

// CachedMembers remembers each user's org set for a short lifetime, so a
// request from a user whose membership was read moments ago costs no ledger
// read (TACK-505). A membership write through this instance forgets the
// user at once; writes made elsewhere reach every instance within one
// lifetime. A failed read caches nothing, so the fail-closed path behind the
// middleware still sees the failure.
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

// AddMember writes through inner and forgets the user's cached set.
func (c *CachedMembers) AddMember(ctx context.Context, m *org.Member) error {
	c.cache.remove(m.UserID.String())
	if err := c.inner.AddMember(ctx, m); err != nil {
		slog.ErrorContext(ctx, "auth.membership_add_failed",
			slog.String("user_id", m.UserID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("add member %s: %w", m.UserID, err)
	}
	return nil
}

// RemoveMember writes through inner and forgets the user's cached set.
func (c *CachedMembers) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	c.cache.remove(userID.String())
	if err := c.inner.RemoveMember(ctx, orgID, userID); err != nil {
		slog.ErrorContext(ctx, "auth.membership_remove_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("remove member %s: %w", userID, err)
	}
	return nil
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
