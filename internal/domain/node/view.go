package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NodeResolve is the global resolution record written atomically with every
// node. It is keyed only by nodeID so any caller can look up an entity by ID
// without org context. The authorization check (does the caller have access
// to this org?) happens after fetch, not before.
//
// Scope context (containing workspace, project, etc.) is derived at read time
// by following child_of relationships, not stored on the resolve record.
type NodeResolve struct {
	OrgID    uuid.UUID `json:"org_id"`
	NodeType string    `json:"node_type"`
}

// NodeView is the denormalized materialized view written atomically with every
// node write. It mirrors Node exactly: same universal fields plus Props. The
// view exists so list operations can scan the view key space and render rows
// without follow-up property or relationship fetches.
//
// Non-indexed Props are omitted from the view to bound its size. Indexed Props
// are copied in full. Relationships are not inlined: callers that need them
// fetch via the RelationshipStore.
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
