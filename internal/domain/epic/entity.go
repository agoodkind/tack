package epic

import (
	"time"

	"github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
)

type Epic struct {
	domain.Base
	WorkspaceID         uuid.UUID
	ProjectID           uuid.UUID
	ParentID            *uuid.UUID
	StateID             *uuid.UUID
	Name                string
	DescriptionHTML     string
	DescriptionStripped string
	Priority            issue.Priority
	SequenceID          int
	SortOrder           float64
	StartDate           *time.Time
	TargetDate          *time.Time
	CompletedAt         *time.Time
	ArchivedAt          *time.Time
	IsDraft             bool
	ExternalSource      *string
	ExternalID          *string
	AssigneeIDs         []uuid.UUID
	LabelIDs            []uuid.UUID
}
