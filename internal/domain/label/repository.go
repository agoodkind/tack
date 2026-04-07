package label

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, label *Label) (*Label, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Label, error)
	List(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]*Label, error)
	Update(ctx context.Context, label *Label) (*Label, error)
	Delete(ctx context.Context, id uuid.UUID) error
}
