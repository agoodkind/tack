package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	// ViewVersion1 is the initial NodeListView schema version.
	ViewVersion1 uint8 = 1
	// MaxSupportedViewVersion is the highest version this binary can read.
	// Bump before releasing a new view schema; old readers see zero-values for
	// new fields, which is valid and triggers async rebuild via NodeViewMigrationWorkflow.
	MaxSupportedViewVersion = ViewVersion1
)

// NodeResolve is a global resolution record written atomically with every entity.
// It is keyed only by nodeID (not org-scoped) so that any service can look up an
// entity by ID without first resolving org context.
// The authorization check (does the caller have access to this org?) happens after
// fetch, not before.
type NodeResolve struct {
	OrgID       uuid.UUID `json:"org_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	ProjectID   uuid.UUID `json:"project_id,omitempty"` // set for project-scoped entities
	NodeType    string    `json:"node_type"`
}

// NodeListView is a denormalized materialized view written atomically with every
// entity write. It holds everything needed to render a list row or respond to a
// Get call without follow-up reads for properties, assignees, or labels.
//
// It is stored at:
//
//	(node_list_view, orgID, workspaceID, nodeType, nodeID) → JSON NodeListView
//
// This mirrors the node_instance key exactly so view and entity records can be
// batch-fetched in parallel futures during the migration transition window.
type NodeListView struct {
	Version     uint8   `json:"v"`
	ID          uuid.UUID `json:"id"`
	OrgID       uuid.UUID `json:"org_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	ProjectID   uuid.UUID `json:"project_id"`
	NodeType    string    `json:"node_type"`
	SequenceID  int32     `json:"sequence_id"`
	SortOrder   float64   `json:"sort_order"`
	Name        string    `json:"name"`
	// Description is truncated at 500 chars to bound view size.
	Description string    `json:"description,omitempty"`

	StateID  *uuid.UUID `json:"state_id,omitempty"`
	ParentID *uuid.UUID `json:"parent_id,omitempty"`
	EpicID   *uuid.UUID `json:"epic_id,omitempty"`

	AssigneeIDs []uuid.UUID `json:"assignee_ids,omitempty"`
	LabelIDs    []uuid.UUID `json:"label_ids,omitempty"`

	// System properties inlined to avoid property key lookups on read.
	Priority  string     `json:"priority,omitempty"`
	StartDate *time.Time `json:"start_date,omitempty"`
	DueDate   *time.Time `json:"due_date,omitempty"`
	IsDraft   bool       `json:"is_draft,omitempty"`
	// Status is used by module nodes only.
	Status string `json:"status,omitempty"`

	// CustomProps holds only Indexed=true PropertyDef values by def name.
	// Non-indexed properties are excluded to bound view size.
	CustomProps map[string]json.RawMessage `json:"custom_props,omitempty"`

	CreatedBy uuid.UUID `json:"created_by"`
	UpdatedBy uuid.UUID `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
