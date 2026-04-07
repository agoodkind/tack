package epic

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, epic *Epic) (*Epic, error)
	GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*Epic, error)
	List(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*Epic, int, error)
	Update(ctx context.Context, epic *Epic) (*Epic, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListIssueIDs(ctx context.Context, epicID uuid.UUID) ([]uuid.UUID, error)
}
