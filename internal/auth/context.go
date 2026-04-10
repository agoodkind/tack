// Package auth provides HTTP middleware for bearer-token authentication and
// context helpers for propagating the authenticated user ID through request context.
package auth

import (
	"context"

	"github.com/google/uuid"
)

type userKey struct{}

// WithUser stores the authenticated user's ID in the context.
// Called by the auth middleware after a successful token lookup.
func WithUser(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

// UserID returns the authenticated user's ID from the context.
// Returns uuid.Nil and false if no user is present (unauthenticated request).
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// MustUserID returns the authenticated user ID or panics.
// Only use in handlers that are guaranteed to be behind the auth middleware.
func MustUserID(ctx context.Context) uuid.UUID {
	id, ok := UserID(ctx)
	if !ok {
		panic("auth: MustUserID called on unauthenticated context")
	}
	return id
}
