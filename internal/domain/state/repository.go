package state

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, state *State) (*State, error)
	GetByID(ctx context.Context, projectID, id uuid.UUID) (*State, error)
	List(ctx context.Context, projectID uuid.UUID) ([]*State, error)
	Update(ctx context.Context, state *State) (*State, error)
	Delete(ctx context.Context, id uuid.UUID) error
}
