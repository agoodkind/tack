package token

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	// Validate looks up an active token that has not expired.
	//
	// Validation does not update last_used. The audit-consumer updates last_used
	// from the authentication event.
	//
	//   - A missing or expired token returns an authentication error.
	Validate(ctx context.Context, raw string) (*Token, error)
	Create(ctx context.Context, t *Token) (*Token, error)
	List(ctx context.Context, userID uuid.UUID) ([]*Token, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}
