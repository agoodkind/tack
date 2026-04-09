package cycle

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, c *Cycle) (*Cycle, error)
	GetByID(ctx context.Context, projectID, id uuid.UUID) (*Cycle, error)
	List(ctx context.Context, projectID uuid.UUID) ([]*Cycle, error)
	Update(ctx context.Context, c *Cycle) (*Cycle, error)
	Delete(ctx context.Context, id uuid.UUID) error
}
