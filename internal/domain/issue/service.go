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
	// Move moves an issue to a different project, reallocating its sequence ID.
	Move(ctx context.Context, workspaceID, projectID, issueID, targetProjectID uuid.UUID, actorID uuid.UUID) (*Issue, error)
	// BulkUpdate applies patch.fields to all issues in patch.IssueIDs atomically.
	BulkUpdate(ctx context.Context, workspaceID uuid.UUID, patch BulkUpdatePatch) (updated int, err error)
	// BulkDelete soft-deletes all issues in issueIDs.
	BulkDelete(ctx context.Context, workspaceID uuid.UUID, issueIDs []uuid.UUID) (deleted int, err error)
	// BulkMove moves each issue to targetProjectID, reallocating sequence IDs.
	// Returns (moved, failed) — failures are per-issue and do not abort the batch.
	BulkMove(ctx context.Context, workspaceID, projectID uuid.UUID, issueIDs []uuid.UUID, targetProjectID uuid.UUID, actorID uuid.UUID) (moved int, failed int, err error)
}
