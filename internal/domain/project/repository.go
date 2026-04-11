package project

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, project *Project) (*Project, error)
	GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*Project, error)
	// GetByIdentifier returns the project with the given short identifier (e.g. "ENG") in a workspace.
	GetByIdentifier(ctx context.Context, workspaceID uuid.UUID, identifier string) (*Project, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]*Project, error)
	Update(ctx context.Context, project *Project) (*Project, error)
	Delete(ctx context.Context, id uuid.UUID) error
	// AllocateSequenceID atomically increments and returns next sequence number for entity type.
	AllocateSequenceID(ctx context.Context, projectID uuid.UUID, entityType string) (int, error)
}
