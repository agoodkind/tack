package tools

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
)

func mustUser(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, errors.New("unauthenticated")
	}
	return id, nil
}
