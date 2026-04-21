package node

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NodeListQuery describes a read request to NodeReader.List or NodeReader.Stream.
//
// The primary index selector picks a scan strategy:
//   - ByProperty: scan the node_by_property index for PropName equal to Value
//   - BySourceRelation: scan relationships from SourceID with RelationType, return
//     the target nodes
//   - ByTargetRelation: scan relationships to TargetID with RelationType, return
//     the source nodes
//   - none set: full scan of (orgID, nodeType) in the view key space
//
// PropFilters apply after the primary scan: the caller's post-scan AND filter
// over Props keys. Non-indexed properties are filtered this way.
type NodeListQuery struct {
	OrgID    uuid.UUID
	NodeType string // optional; empty = all types under OrgID

	// Primary index selector: set at most one.
	ByProperty       *PropertyMatch
	BySourceRelation *RelationScan
	ByTargetRelation *RelationScan

	// UUIDv7 time-range boundaries; no extra index needed.
	CreatedAfter  *time.Time
	CreatedBefore *time.Time

	// Post-scan Props filters. AND across keys; each entry matches when the
	// node's Props[key] equals Value (raw JSON equality).
	PropFilters []PropertyMatch

	// Pagination.
	Limit  int
	Cursor string
}

// PropertyMatch pairs a PropertyDef name with an expected raw JSON value.
// Raw JSON equality is used for indexed scans and post-filter comparisons.
type PropertyMatch struct {
	PropName string
	Value    json.RawMessage
}

// RelationScan selects nodes via a relationship scan.
type RelationScan struct {
	NodeID       uuid.UUID
	RelationType string
}

// NodeStreamResult is one item delivered through a NodeReader.Stream channel.
type NodeStreamResult struct {
	View *NodeView
	Err  error
}

// NodeReader is the read path for all node types. The service layer MUST use
// NodeReader for reads; it must not call storage-layer repositories directly.
type NodeReader interface {
	// Get resolves nodeID via the global resolve record and fetches the NodeView.
	// Does not require org context from the caller. The caller authorizes the
	// returned OrgID.
	Get(ctx context.Context, nodeID uuid.UUID) (*NodeView, error)

	// Resolve returns the org / type context for any node UUID.
	Resolve(ctx context.Context, nodeID uuid.UUID) (*NodeResolve, error)

	// List is for bounded result sets. Blocks until all matching views assemble.
	List(ctx context.Context, q NodeListQuery) ([]*NodeView, error)

	// Stream is for large or unbounded scans.
	Stream(ctx context.Context, q NodeListQuery) (<-chan NodeStreamResult, error)
}
