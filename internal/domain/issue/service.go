package issue

import (
	"context"

	"github.com/google/uuid"
)

type Service interface {
	Create(ctx context.Context, issue *Issue) (*Issue, error)
	Get(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*Issue, error)
	List(ctx context.Context, filter ListFilter) ([]*Issue, int, error)
	Update(ctx context.Context, issue *Issue) (*Issue, error)
	Delete(ctx context.Context, workspaceID, projectID, id uuid.UUID) error
	SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error
	Search(ctx context.Context, workspaceID uuid.UUID, q string, filter ListFilter) ([]*Issue, int, error)
}
