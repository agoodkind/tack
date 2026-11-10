package node

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NodeListQuery specifies a read request to NodeReader.List or
// NodeReader.Stream. The primary selector controls the scan:
//
//   - ByProperty scans node_by_property for PropName equal to Value.
//   - BySourceRelation scans relationships from its NodeID with RelationType
//     and returns their target nodes.
//   - ByTargetRelation scans relationships to its NodeID with RelationType
//     and returns their source nodes.
//   - An unset selector scans the full (orgID, nodeType) view key range.
//
// PropFilters checks the Props keys after the primary scan. Every filter must
// match. This step supports properties without an index.
type NodeListQuery struct {
	OrgID    uuid.UUID
	NodeType string // NodeType selects one type; an empty value selects all types under OrgID.

	// Set at most one primary index selector.
	ByProperty       *PropertyMatch
	BySourceRelation *RelationScan
	ByTargetRelation *RelationScan

	// CreatedAfter and CreatedBefore bound UUIDv7 creation times without an
	// extra index.
	CreatedAfter  *time.Time
	CreatedBefore *time.Time

	// PropFilters checks Props after the scan. Every entry must match the raw
	// JSON value at its property key.
	PropFilters []PropertyMatch

	// Limit and Cursor control pagination.
	Limit  int
	Cursor string
}

// PropertyMatch pairs a PropertyDef name with an expected raw JSON value.
// Indexed scans and comparisons after filtering use raw JSON equality.
type PropertyMatch struct {
	PropName string
	Value    json.RawMessage
}

// RelationScan selects nodes via a relationship scan.
type RelationScan struct {
	NodeID       uuid.UUID
	RelationType string
}

// NodeStreamResult contains one item from a NodeReader.Stream channel.
type NodeStreamResult struct {
	View *NodeView
	Err  error
}

// Page contains one page from ListPage. NextCursor is empty on the last page.
type Page struct {
	Views      []*NodeView
	NextCursor string
}

// NodeReader provides reads for every node type. The service layer must use
// NodeReader for reads and must not read directly from storage repositories.
type NodeReader interface {
	// Get resolves nodeID with the global resolve record and reads the NodeView.
	// The caller does not supply organization context and must authorize the
	// returned OrgID.
	Get(ctx context.Context, nodeID uuid.UUID) (*NodeView, error)

	// Resolve returns the organization and type context for any node UUID.
	Resolve(ctx context.Context, nodeID uuid.UUID) (*NodeResolve, error)

	// List is for bounded result sets. It waits until all matching views assemble.
	List(ctx context.Context, q NodeListQuery) ([]*NodeView, error)

	// Stream supports large or unbounded scans.
	Stream(ctx context.Context, q NodeListQuery) (<-chan NodeStreamResult, error)

	// ListPage returns at most q.Limit views after q.Cursor, in node ID
	// order. It supports ByProperty and full type scans.
	ListPage(ctx context.Context, q NodeListQuery) (Page, error)
}
