package node

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// NodeCleanupScheduler enqueues asynchronous FoundationDB cleanup for a
// soft-deleted node.
type NodeCleanupScheduler interface {
	Schedule(ctx context.Context, orgID, nodeID uuid.UUID) error
}

// NodeDeleter deletes a node in bounded transactions. The child_of edges
// determine its hierarchy descendants. A direct child moves to the deleted
// node's parent when the child's CanLiveUnder list or the parent's CanContain
// list permits that type pair. Otherwise, the job deletes the child and its
// descendants.
type NodeDeleter interface {
	// StartSubtreeDelete stores a new job record with job.RootID as the only
	// stack entry and the encoded audit operation from ctx. It returns an error
	// when audit.RequireMutationContext cannot read a valid operation from ctx.
	StartSubtreeDelete(ctx context.Context, job *SubtreeDeleteJob) error
	// DeleteSubtreeStep runs one bounded job step. The step moves one eligible
	// direct child to the root's parent, deletes one node, adds one hierarchy
	// child to the stack, or clears one edge page when the node exceeds the
	// step's edge bound. Each changed node or relationship produces a change
	// event with the actor and operation ID from the job record.
	DeleteSubtreeStep(ctx context.Context, jobID uuid.UUID) (SubtreeDeleteProgress, error)
	// SubtreeDelete reads one job record and returns nil when it does not exist.
	SubtreeDelete(ctx context.Context, jobID uuid.UUID) (*SubtreeDeleteJob, error)
	// SubtreeDeletes reads at most limit records in job ID order.
	// It starts after the job ID in after; uuid.Nil starts with the first job.
	SubtreeDeletes(ctx context.Context, after uuid.UUID, limit int) ([]*SubtreeDeleteJob, error)
	// ClearSubtreeDelete removes the record of one finished job.
	ClearSubtreeDelete(ctx context.Context, jobID uuid.UUID) error
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

// RelationshipChanges contains relationship mutations for a node update.
type RelationshipChanges struct {
	// Add lists relationships written in the same transaction as the node update.
	Add []*Relationship
	// Remove lists relationships to clear in the transaction that updates
	// the node.
	Remove []*Relationship
}

// ReferenceKey identifies one rendered reference owned by a node.
type ReferenceKey struct {
	// TemplateName identifies the declaring reference template.
	TemplateName string
	// Encoded is the template's rendered reference.
	Encoded string
}

// NodeRepository provides the single write path for nodes. CreateAtomic
// writes the primary record, view, resolve record, property indexes for
// indexed Props, and all supplied relationships in one FoundationDB
// transaction.
//
// Callers supply Props and Relationships directly. The interface does not
// accept concept-specific parameters such as assigneeIDs, labelIDs, or state
// transitions.
type NodeRepository interface {
	// Get returns a node by primary key, or nil, nil when the node does not exist.
	Get(ctx context.Context, orgID, nodeID uuid.UUID) (*Node, error)

	// Set overwrites an existing node and view without reconciling secondary
	// property indexes. Use UpdateAtomic for every user-facing update. Use Set
	// only after the caller reconciles the indexes separately.
	Set(ctx context.Context, n *Node, view *NodeView) error

	// UpdateAtomic overwrites the node and view and reconciles secondary
	// property indexes against oldProps for each name in indexedProps.
	//
	// The referenceKeys argument controls reference ownership:
	//
	//   - A nil slice preserves the node's existing claims.
	//   - A non-nil slice replaces the claims.
	//   - A non-nil empty slice releases every claim.
	//
	// A duplicate reference aborts the entire update. Concurrent readers cannot
	// observe a partially replaced reference index.
	UpdateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		oldProps map[string]json.RawMessage,
		indexedProps []string,
		referenceKeys []ReferenceKey,
		relationshipChanges ...RelationshipChanges,
	) error

	// Delete removes the primary node, view, resolve record, all property
	// indexes, and all relationships where the node is source or target.
	Delete(ctx context.Context, orgID, nodeID uuid.UUID) error

	// SetReferenceKeys replaces the rendered references owned by a node.
	SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []ReferenceKey) error

	// LookupReference returns the node that owns a rendered reference.
	// It returns uuid.Nil and nil when the lookup does not find an owner.
	LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (uuid.UUID, error)

	// CreateAtomic writes a new node and its initial relationships in one
	// FoundationDB transaction. NodeService.Create calls CreateAtomic. When the
	// node declares FeatureHasSequenceID, the caller allocates the sequence and
	// sets its value in Props before calling CreateAtomic.
	//
	// The indexedProps argument selects the Props that need secondary property
	// index entries. The caller reads the PropertyDef registry to select these
	// properties before calling CreateAtomic. The storage layer does not read
	// PropertyDefs.
	CreateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		rels []*Relationship,
		indexedProps []string,
		referenceKeys []ReferenceKey,
		idempotency *IdempotencyRecord,
	) error

	// ListByProperty returns every node with a property equal to value. The
	// PropertyDef must have Indexed=true. An empty nodeType searches all types;
	// a nonempty nodeType limits the scan to that type as an optimization.
	ListByProperty(
		ctx context.Context,
		orgID uuid.UUID,
		nodeType string,
		propName string,
		value json.RawMessage,
	) ([]*Node, error)

	// AllocateSequence atomically increments the counter for
	// (orgID, scopeNodeID, nodeType) and returns its next value. The scopeNodeID
	// identifies the container, typically a project node, within which sequence
	// numbers are unique.
	AllocateSequence(ctx context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (int64, error)

	// AllocateSequenceByKey atomically increments the counter at counterKey and
	// returns the next value.
	AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (int64, error)

	// SeedSequenceByKey raises counterKey to value without lowering it.
	SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) error

	// LookupIdempotencyKey returns the record for (orgID, key). It returns a nil
	// record and nil error when the key does not have a record.
	LookupIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (*IdempotencyRecord, error)
}

// RelationshipRepository manages edges between nodes. Relationship records
// with appropriate RelationType strings represent assignments, labels on
// nodes, containment, watchers, mentions, reactions, and comments.
type RelationshipRepository interface {
	Add(ctx context.Context, rel *Relationship) error
	Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error

	// ListBySource returns relationships with sourceID as their source. A
	// nonempty relationType filters the results; an empty string accepts all types.
	ListBySource(ctx context.Context, orgID, sourceID uuid.UUID, relationType string) ([]*Relationship, error)

	// ListByTarget returns relationships with targetID as their target. A
	// nonempty relationType filters the results; an empty string accepts all types.
	ListByTarget(ctx context.Context, orgID, targetID uuid.UUID, relationType string) ([]*Relationship, error)
}
