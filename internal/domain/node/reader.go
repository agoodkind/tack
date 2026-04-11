package node

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NodeListQuery describes a read request to NodeReader.List or NodeReader.Stream.
// Exactly one of ByProject, ByState, or ByProperty should be set for indexed scans.
// When none are set, all nodes of NodeType in the workspace are returned.
type NodeListQuery struct {
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	NodeType    string

	// Primary index selector — set exactly one for an indexed scan.
	ByProject  *uuid.UUID
	ByState    *uuid.UUID
	ByProperty *PropertyFilter

	// Time-range filters translate to UUIDv7 boundary range scans on nodeID.
	// No extra index needed: UUIDv7 high bits embed Unix milliseconds.
	CreatedAfter  *time.Time
	CreatedBefore *time.Time

	// Post-scan in-memory filters applied after chunk fetch.
	FilterEpicID   *uuid.UUID
	FilterIsDraft  *bool
	FilterAssignee *uuid.UUID

	// Pagination — enforced at the storage layer.
	// 0 = no limit; use Stream for unbounded scans.
	Limit  int
	Cursor string
}

// PropertyFilter selects nodes where a given indexed property equals Value.
type PropertyFilter struct {
	PropDefID uuid.UUID
	Value     *PropertyValue
}

// NodeStreamResult is one item delivered through a NodeReader.Stream channel.
type NodeStreamResult struct {
	View *NodeListView
	Err  error
}

// NodeReader is the read path for all node types.
// The service layer MUST use NodeReader for all reads; it must NOT call
// EntityRepository, PropertyRepository, AssignmentRepository, or LabelRepository
// directly for read operations.
//
// The chunking and parallelism are private implementation details of ViewStore.
// Callers see records; they never see chunk boundaries or pagination tokens.
type NodeReader interface {
	// Get resolves nodeID via the global resolution record (node_resolve key),
	// then fetches the NodeListView. Does not require org context from the caller.
	// The caller is responsible for authorizing the returned OrgID.
	Get(ctx context.Context, nodeID uuid.UUID) (*NodeListView, error)

	// List is for bounded result sets. It blocks until all matching views are
	// assembled (parallel chunk fetches) and returns them as a single slice.
	// Use Stream for large or open-ended queries.
	List(ctx context.Context, q NodeListQuery) ([]*NodeListView, error)

	// Stream is for large or unbounded scans. It returns a buffered channel
	// immediately. Views are sent as parallel chunks complete. The channel is
	// closed when all chunks finish or when any chunk returns an error (the
	// error is delivered on the channel before close).
	// MCP tools pump the channel and assemble the final response; chunking is
	// invisible to the tool handler.
	Stream(ctx context.Context, q NodeListQuery) (<-chan NodeStreamResult, error)
}
