package module

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, m *Module) (*Module, error)
	GetByID(ctx context.Context, projectID, id uuid.UUID) (*Module, error)
	List(ctx context.Context, projectID uuid.UUID) ([]*Module, error)
	Update(ctx context.Context, m *Module) (*Module, error)
	Delete(ctx context.Context, id uuid.UUID) error
	AddIssues(ctx context.Context, moduleID uuid.UUID, issueIDs []uuid.UUID) error
	RemoveIssue(ctx context.Context, moduleID, issueID uuid.UUID) error
	ListIssueIDs(ctx context.Context, moduleID uuid.UUID) ([]uuid.UUID, error)
}
