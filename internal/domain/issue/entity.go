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
	NodeID         uuid.UUID  `json:"node_id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id"`
	ProjectID      uuid.UUID  `json:"project_id"`
	EpicID         *uuid.UUID `json:"epic_id,omitempty"`
	ParentID       *uuid.UUID `json:"parent_id,omitempty"`
	StateID        *uuid.UUID `json:"state_id,omitempty"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Priority       Priority   `json:"priority"`
	SequenceID     int        `json:"sequence_id"`
	SortOrder      float64    `json:"sort_order"`
	StartDate      *time.Time `json:"start_date,omitempty"`
	TargetDate     *time.Time `json:"target_date,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	IsDraft        bool       `json:"is_draft"`
	ExternalSource *string    `json:"external_source,omitempty"`
	ExternalID     *string    `json:"external_id,omitempty"`
	AssigneeIDs    []uuid.UUID `json:"assignee_ids,omitempty"`
	LabelIDs       []uuid.UUID `json:"label_ids,omitempty"`
}
