package issue

import (
	"context"

	"github.com/google/uuid"
)

// BulkUpdatePatch describes the fields to apply across a set of issues.
// A nil pointer means "don't change this field".
// SetEpicID must be true to apply EpicID (allows clearing with a nil EpicID).
// AssigneeIDs nil means "don't change"; non-nil (including empty) replaces.
type BulkUpdatePatch struct {
	IssueIDs    []uuid.UUID
	ProjectID   uuid.UUID // required when AssigneeIDs is non-nil (needed to look up nodeIDs)
	StateID     *uuid.UUID
	Priority    *Priority
	EpicID      *uuid.UUID
	SetEpicID   bool
	AssigneeIDs []uuid.UUID
	ActorID     uuid.UUID
}

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
	// GetByIDs fetches all non-deleted issues in workspaceID whose IDs are in ids.
	// Used to hydrate Meilisearch search results.
	GetByIDs(ctx context.Context, workspaceID uuid.UUID, ids []uuid.UUID) ([]*Issue, error)
	List(ctx context.Context, filter ListFilter) ([]*Issue, int, error)
	Update(ctx context.Context, issue *Issue) (*Issue, error)
	Delete(ctx context.Context, id uuid.UUID) error
	SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error
	Search(ctx context.Context, workspaceID uuid.UUID, q string, filter ListFilter) ([]*Issue, int, error)
	// Move changes an issue's project, allocating newSequenceID in the target project.
	// state_id is cleared; the caller is responsible for allocating the sequence ID.
	Move(ctx context.Context, issueID, targetProjectID uuid.UUID, newSequenceID int, actorID uuid.UUID) (*Issue, error)
	// BulkUpdate applies patch to the issues identified in patch.IssueIDs in a single SQL statement.
	BulkUpdate(ctx context.Context, patch BulkUpdatePatch) (updated int, err error)
	// BulkDelete soft-deletes all issues in issueIDs in a single SQL statement.
	// Returns the node_ids of deleted issues for FDB side-effect cleanup.
	BulkDelete(ctx context.Context, issueIDs []uuid.UUID) (nodeIDs []uuid.UUID, err error)
}
