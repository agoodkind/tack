package workspace

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, w *Workspace) (*Workspace, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Workspace, error)
	GetBySlug(ctx context.Context, slug string) (*Workspace, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*Workspace, error)
}
