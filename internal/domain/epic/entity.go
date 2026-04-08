package epic

import (
	"time"

	"github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
)

type Epic struct {
	domain.Base
	NodeID      uuid.UUID
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ParentID    *uuid.UUID
	StateID     *uuid.UUID
	Name        string
	Description string // Markdown TEXT; not selected on list queries
	Priority    issue.Priority
	SequenceID  int
	SortOrder   float64
	StartDate   *time.Time
	TargetDate  *time.Time
	CompletedAt *time.Time
	ArchivedAt  *time.Time
	IsDraft     bool
	AssigneeIDs []uuid.UUID
	LabelIDs    []uuid.UUID
}
