package node

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// NodeCleanupScheduler enqueues asynchronous FDB cleanup for a soft-deleted node.
type NodeCleanupScheduler interface {
	Schedule(ctx context.Context, orgID, nodeID uuid.UUID) error
}

// NodeDeleter clears every FDB record keyed by a node's ID: the primary Node,
// the view, resolve, property-value index entries, and all Relationships where
// the node is source or target.
type NodeDeleter interface {
	DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error
}

// TypeRepository manages NodeType definitions.
type TypeRepository interface {
	Set(ctx context.Context, nt *NodeType) error
	Get(ctx context.Context, orgID, typeID uuid.UUID) (*NodeType, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*NodeType, error)
	Delete(ctx context.Context, orgID, typeID uuid.UUID) error
}

// PropertyDefRepository manages PropertyDef records.
type PropertyDefRepository interface {
	Set(ctx context.Context, def *PropertyDef) error
	Get(ctx context.Context, orgID, defID uuid.UUID) (*PropertyDef, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*PropertyDef, error)
	Delete(ctx context.Context, orgID, defID uuid.UUID) error
}

// NodeRepository is the single write path for nodes. CreateAtomic writes the
// node primary record, the view, the resolve record, property indexes for
// indexed Props, and all provided relationships in a single FDB transaction.
//
// No concept-specific parameters (no assigneeIDs, no labelIDs, no state
// transitions). Callers pass Props and Relationships directly.
type NodeRepository interface {
	// Get returns a node by primary key. Returns nil, nil if not found.
	Get(ctx context.Context, orgID, nodeID uuid.UUID) (*Node, error)

	// Set overwrites an existing node and view but does NOT reconcile the
	// secondary property indexes. Use UpdateAtomic on every user-facing
	// update path. Set is correct only when the caller has already
	// reconciled indexes externally.
	Set(ctx context.Context, n *Node, view *NodeView) error

	// UpdateAtomic overwrites the node and view, then reconciles the
	// secondary property indexes against oldProps for each name in
	// indexedProps. Single FDB transaction so concurrent readers never see
	// a half-rotated index.
	UpdateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		oldProps map[string]json.RawMessage,
		indexedProps []string,
	) error

	// Delete removes the primary node, view, resolve record, all property
	// indexes, and all relationships where the node is source or target.
	Delete(ctx context.Context, orgID, nodeID uuid.UUID) error

	// CreateAtomic writes a new node plus its initial relationships in one FDB
	// transaction. Used by NodeService.Create. Sequence allocation (if the node
	// declares FeatureHasSequenceID) is the caller's responsibility; the caller
	// sets the sequence value in Props before calling CreateAtomic.
	//
	// indexedProps names the subset of Props that should receive a secondary
	// property index entry. The caller resolves this from the PropertyDef
	// registry before calling; the storage layer does not read PropertyDefs.
	CreateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		rels []*Relationship,
		indexedProps []string,
	) error

	// ListByProperty returns all nodes where the given property equals value.
	// Only works for PropertyDefs with Indexed=true. NodeType scoping is an
	// optimization: pass empty to scan across types.
	ListByProperty(
		ctx context.Context,
		orgID uuid.UUID,
		nodeType string,
		propName string,
		value json.RawMessage,
	) ([]*Node, error)

	// AllocateSequence atomically increments a counter keyed by (orgID, scopeNodeID, nodeType)
	// and returns the next value. The scopeNodeID is the container (typically a
	// project node) under which sequence numbers are unique.
	AllocateSequence(ctx context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (int64, error)

	// GetSlug returns the nodeID registered under a global (nodeType, slug) pair.
	// Used for entry-point lookups like workspace-by-slug.
	GetSlug(ctx context.Context, nodeType, slug string) (uuid.UUID, error)
	// WriteSlug returns domain.ErrAlreadyExists when the slug is already
	// owned by a different node. Idempotent when the existing owner matches.
	WriteSlug(ctx context.Context, nodeType, slug string, nodeID uuid.UUID) error
	DeleteSlug(ctx context.Context, nodeType, slug string) error

	// LookupIdempotencyKey returns the nodeID a previous Create stamped under
	// (orgID, key). uuid.Nil and a nil error mean the key has not been seen.
	LookupIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (uuid.UUID, error)
	// WriteIdempotencyKey records (orgID, key) -> nodeID. Used inside
	// CreateAtomic so retries with the same key short-circuit. The store does
	// not GC entries; sentinels live forever.
	WriteIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string, nodeID uuid.UUID) error
}

// RelationshipRepository manages edges between nodes. Every dedicated
// concept-specific join (assignments, labels-on-nodes, containment, watchers,
// mentions, reactions, comments) is expressed as a Relationship with an
// appropriate RelationType string.
type RelationshipRepository interface {
	Add(ctx context.Context, rel *Relationship) error
	Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error

	// ListBySource returns all relationships originating from sourceID, optionally
	// filtered by relationType (empty string = all types).
	ListBySource(ctx context.Context, orgID, sourceID uuid.UUID, relationType string) ([]*Relationship, error)

	// ListByTarget returns all relationships pointing to targetID, optionally
	// filtered by relationType (empty string = all types).
	ListByTarget(ctx context.Context, orgID, targetID uuid.UUID, relationType string) ([]*Relationship, error)
}
