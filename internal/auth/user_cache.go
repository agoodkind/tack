package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/user"
)

// CachedUsers caches user records by ID for a short lifetime to avoid ledger
// reads when rendering a node's creator or editor. It caches only GetByID;
// GetByEmail serves operator commands. Display-name changes become visible
// after the cached entry expires. Failed reads do not populate the cache.
// CachedUsers does not provide a Create method.
type CachedUsers struct {
	inner    user.Repository
	cache    *entryCache[*user.User]
	lifetime time.Duration
}

// NewCachedUsers wraps inner.
//
// A lifetime of zero or less disables caching. A size of zero or less
// selects an entry limit of one.
//
// The user-cache tests configure a one-minute lifetime and 16 entries:
//
//	cached := NewCachedUsers(store, time.Minute, 16)
func NewCachedUsers(inner user.Repository, lifetime time.Duration, size int) *CachedUsers {
	if size <= 0 {
		size = 1
	}
	return &CachedUsers{inner: inner, cache: newEntryCache[*user.User](size), lifetime: lifetime}
}

// GetByID returns a cached record when the entry is valid.
//
// Otherwise, it reads inner. Callers receive a record separate from the
// cached copy. A failed read does not populate the cache.
//
// The return values distinguish these outcomes:
//   - A successful read returns the repository record or a cached copy.
//   - A failed read returns nil and an error that includes id.
func (c *CachedUsers) GetByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	now := clock.Now()
	key := id.String()
	if c.lifetime > 0 {
		if cached, ok := c.cache.get(key, now); ok {
			record := *cached
			return &record, nil
		}
	}
	record, err := c.inner.GetByID(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "auth.user_read_failed", slog.String("user_id", id.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("get user %s: %w", id, err)
	}
	if c.lifetime > 0 && record != nil {
		stored := *record
		c.cache.put(key, &stored, now.Add(c.lifetime))
	}
	return record, nil
}

// GetByEmail looks up a user by email.
//
// Each call queries inner independently of GetByID's cache and does not
// cache the result.
//
// The return values distinguish these outcomes:
//   - A successful call returns the repository's record and a nil error.
//   - A failed call returns nil and wraps the repository error.
func (c *CachedUsers) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	record, err := c.inner.GetByEmail(ctx, email)
	if err != nil {
		slog.ErrorContext(ctx, "auth.user_read_by_email_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return record, nil
}
