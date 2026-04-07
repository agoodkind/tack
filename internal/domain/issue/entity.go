package issue

import (
	"time"

	"github.com/agoodkind/tack/internal/domain"
	"github.com/google/uuid"
)

type Priority string

const (
	PriorityNone   Priority = "none"
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

type Issue struct {
	domain.Base
	WorkspaceID         uuid.UUID
	ProjectID           uuid.UUID
	EpicID              *uuid.UUID
	ParentID            *uuid.UUID
	StateID             *uuid.UUID
	Name                string
	DescriptionHTML     string
	DescriptionStripped string
	Priority            Priority
	SequenceID          int
	SortOrder           float64
	StartDate           *time.Time
	TargetDate          *time.Time
	CompletedAt         *time.Time
	ArchivedAt          *time.Time
	IsDraft             bool
	ExternalSource      *string
	ExternalID          *string
	// Populated via join on read
	AssigneeIDs []uuid.UUID
	LabelIDs    []uuid.UUID
}
