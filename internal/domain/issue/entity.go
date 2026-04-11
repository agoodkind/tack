package issue

import (
	"time"

	"goodkind.io/tack/internal/domain"
	"github.com/google/uuid"
)

// ListFilter describes criteria for listing issues. All fields are optional
// except WorkspaceID. Applied in-memory or via FDB index depending on which
// fields are set.
type ListFilter struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	EpicID      *uuid.UUID
	StateIDs    []uuid.UUID
	Priorities  []Priority
	AssigneeIDs []uuid.UUID
	IsDraft     *bool
	PerPage     int
}

// BulkUpdatePatch describes the changes to apply to a set of issues atomically.
// Only non-nil/non-zero fields are applied.
type BulkUpdatePatch struct {
	IssueIDs    []uuid.UUID
	ProjectID   uuid.UUID
	ActorID     uuid.UUID
	StateID     *uuid.UUID
	Priority    *Priority
	SetEpicID   bool       // true means EpicID field should be written (even if nil = clear)
	EpicID      *uuid.UUID // nil + SetEpicID=true clears the epic
	AssigneeIDs []uuid.UUID
}

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
	// AssigneeIDs and LabelIDs are populated from FDB, not from SQL join tables.
	// They are not persisted by the postgres adapter — use fdb.AssignmentStore and fdb.NodeLabelStore.
	AssigneeIDs []uuid.UUID `json:"assignee_ids,omitempty"`
	LabelIDs    []uuid.UUID `json:"label_ids,omitempty"`
}
