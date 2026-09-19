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

// CachedUsers remembers user records by id for a short lifetime, so a tool
// call that renders who created or changed a node costs no ledger read when
// that user was rendered moments ago (criterion 12 of the backup
// acceptance). Only GetByID is cached: the by-email lookup serves operator
// commands, and a create forgets nothing because a new id cannot be cached.
// A display-name change made elsewhere reaches every instance within one
// lifetime. A failed read caches nothing.
type CachedUsers struct {
	inner    user.Repository
	cache    *entryCache[*user.User]
	lifetime time.Duration
}

// NewCachedUsers wraps inner. A lifetime of zero or less returns a wrapper
// that caches nothing.
func NewCachedUsers(inner user.Repository, lifetime time.Duration, size int) *CachedUsers {
	if size <= 0 {
		size = 1
	}
	return &CachedUsers{inner: inner, cache: newEntryCache[*user.User](size), lifetime: lifetime}
}

// GetByID answers from the cache when it can, and asks inner otherwise.
// Callers get their own copy of the record.
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

// GetByEmail reads through inner.
func (c *CachedUsers) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	record, err := c.inner.GetByEmail(ctx, email)
	if err != nil {
		slog.ErrorContext(ctx, "auth.user_read_by_email_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return record, nil
}

// Create writes through inner.
func (c *CachedUsers) Create(ctx context.Context, u *user.User) (*user.User, error) {
	record, err := c.inner.Create(ctx, u)
	if err != nil {
		slog.ErrorContext(ctx, "auth.user_create_failed", slog.String("user_id", u.ID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("create user %s: %w", u.ID, err)
	}
	return record, nil
}
