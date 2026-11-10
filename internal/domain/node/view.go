package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NodeResolve stores a global resolution record that node writes create
// atomically. Its key uses only nodeID; callers can resolve an entity
// without organization context. Authorization checks the organization after
// the read.
//
// Reads derive containing workspace and project context from child_of
// relationships. The record does not store this scope context.
type NodeResolve struct {
	OrgID    uuid.UUID `json:"org_id"`
	NodeType string    `json:"node_type"`
}

// NodeView stores the universal Node fields and indexed Props for list
// reads. Each node write updates the view atomically. List operations scan
// view keys to render rows without separate property or relationship reads.
//
// The view excludes non-indexed Props to limit size and copies indexed Props
// in full. Callers that need relationships read them separately through
// RelationshipStore.
type NodeView struct {
	ID        uuid.UUID                  `json:"id"`
	OrgID     uuid.UUID                  `json:"org_id"`
	NodeType  string                     `json:"node_type"`
	Name      string                     `json:"name"`
	Props     map[string]json.RawMessage `json:"props,omitempty"`
	CreatedBy uuid.UUID                  `json:"created_by"`
	UpdatedBy uuid.UUID                  `json:"updated_by"`
	CreatedAt time.Time                  `json:"created_at"`
	UpdatedAt time.Time                  `json:"updated_at"`
}
