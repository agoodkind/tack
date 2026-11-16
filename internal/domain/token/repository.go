package token

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	// Validate looks up an active, non-expired token without updating last_used.
	Validate(ctx context.Context, raw string) (*Token, error)
	Create(ctx context.Context, t *Token) (*Token, error)
	List(ctx context.Context, userID uuid.UUID) ([]*Token, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}
