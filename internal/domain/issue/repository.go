package issue

import (
	"context"

	"github.com/google/uuid"
)

type ListFilter struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	EpicID      *uuid.UUID
	ModuleID    *uuid.UUID
	CycleID     *uuid.UUID
	StateIDs    []uuid.UUID
	Priorities  []Priority
	AssigneeIDs []uuid.UUID
	LabelIDs    []uuid.UUID
	IsDraft     *bool
	Cursor      *string
	PerPage     int
	OrderBy     string
}

type Repository interface {
	Create(ctx context.Context, issue *Issue) (*Issue, error)
	GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*Issue, error)
	List(ctx context.Context, filter ListFilter) ([]*Issue, int, error)
	Update(ctx context.Context, issue *Issue) (*Issue, error)
	Delete(ctx context.Context, id uuid.UUID) error
	SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error
	Search(ctx context.Context, workspaceID uuid.UUID, q string, filter ListFilter) ([]*Issue, int, error)
}
