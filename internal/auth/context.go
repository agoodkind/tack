// Package auth provides HTTP middleware for bearer-token authentication. Its
// context helpers store and read the authenticated user ID.
package auth

import (
	"context"

	"github.com/google/uuid"
)

type userKey struct{}

// WithUser stores the authenticated user's ID in the context.
//
// The auth middleware calls WithUser after a successful token lookup or
// development UUID parse. The returned context provides the ID to UserID:
//
//	ctx = WithUser(ctx, userID)
//	id, authenticated := UserID(ctx)
func WithUser(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

// UserID returns the authenticated user's ID from the context.
//
// It returns [uuid.Nil] and false when the context does not contain a user
// ID. A stored [uuid.Nil] also produces false.
//
// Callers can check the second result before using the ID:
//
//	userID, authenticated := UserID(ctx)
//	if !authenticated {
//		return
//	}
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
