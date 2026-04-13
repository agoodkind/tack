package node

import (
	"context"

	"github.com/google/uuid"
)

// NodeCleanupScheduler enqueues asynchronous FDB cleanup for a soft-deleted node.
// The real implementation enqueues a Temporal workflow. The noop does nothing.
type NodeCleanupScheduler interface {
	Schedule(ctx context.Context, orgID, nodeID uuid.UUID) error
}

// NodeDeleter cleans up all FDB-resident data for a node when it is deleted.
// This covers assignments, labels, comments, watchers, counters, relations, and
// all other key spaces keyed by node_id. Entity-specific SQL cascades are handled
// separately by each entity's repository.
//
// Keys where node_id is NOT the primary scope (draft_for_user_on_node,
// sort_position_in_view, activity_by_user, work_log_by_user) are left as
// low-priority orphans and are cleaned by a GC pass, not on every delete.
type NodeDeleter interface {
	DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error
}

// TypeRepository manages user-defined node type definitions.
type TypeRepository interface {
	Set(ctx context.Context, nt *NodeType) error
	Get(ctx context.Context, orgID, typeID uuid.UUID) (*NodeType, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*NodeType, error)
	Delete(ctx context.Context, orgID, typeID uuid.UUID) error
}

// PropertyRepository manages property definitions and per-node values.
type PropertyRepository interface {
	SetDef(ctx context.Context, def *PropertyDef) error
	GetDef(ctx context.Context, orgID, defID uuid.UUID) (*PropertyDef, error)
	ListDefs(ctx context.Context, orgID, workspaceID uuid.UUID, projectID *uuid.UUID) ([]*PropertyDef, error)
	DeleteDef(ctx context.Context, def *PropertyDef) error

	SetValue(ctx context.Context, orgID, nodeID, propDefID uuid.UUID, value any) error
	GetValues(ctx context.Context, orgID, nodeID uuid.UUID) (Properties, error)
	DeleteValue(ctx context.Context, orgID, nodeID, propDefID uuid.UUID) error
}

// ActivityRepository is an append-only log of node change events.
type ActivityRepository interface {
	Append(ctx context.Context, orgID, workspaceID uuid.UUID, event *ActivityEvent) error
	List(ctx context.Context, orgID, workspaceID, nodeID uuid.UUID) ([]*ActivityEvent, error)
}

// AssignmentRepository manages who is assigned to what.
type AssignmentRepository interface {
	// SetAll replaces all assignees on a node atomically.
	SetAll(ctx context.Context, orgID, nodeID uuid.UUID, userIDs []uuid.UUID, assignedBy uuid.UUID) error
	// ListByNode returns all userIDs assigned to a node.
	ListByNode(ctx context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error)
	// ListByUser returns all nodeIDs assigned to a user.
	ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]uuid.UUID, error)
}

// NodeLabelRepository manages labels on any node type.
type NodeLabelRepository interface {
	// SetAll replaces all labels on a node atomically.
	SetAll(ctx context.Context, orgID, nodeID uuid.UUID, labelIDs []uuid.UUID, addedBy uuid.UUID) error
	// ListByNode returns all labelIDs on a node.
	ListByNode(ctx context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error)
	// ListByLabel returns all nodeIDs with a given label.
	ListByLabel(ctx context.Context, orgID, labelID uuid.UUID) ([]uuid.UUID, error)
}

// ContainmentRepository manages parent-child containment between any node types.
// A single generic pattern replaces per-type module/cycle/epic join logic.
type ContainmentRepository interface {
	// AddChild links a child node to a container node.
	AddChild(ctx context.Context, orgID, containerID, childID, addedBy uuid.UUID) error
	// RemoveChild unlinks a child node from a container node.
	RemoveChild(ctx context.Context, orgID, containerID, childID uuid.UUID) error
	// ListChildren returns all child node IDs in a container.
	ListChildren(ctx context.Context, orgID, containerID uuid.UUID) ([]uuid.UUID, error)
	// ListContainers returns all container node IDs that contain the given child.
	ListContainers(ctx context.Context, orgID, childID uuid.UUID) ([]uuid.UUID, error)
}

// EntityRepository stores NodeValue instances in FDB and maintains secondary
// indexes for project, state, and any indexed property.
type EntityRepository interface {
	// Set writes or overwrites a NodeValue and its typed properties atomically.
	// props may be nil to write only the NodeValue without touching properties.
	// view may be nil; when non-nil it is written atomically alongside the entity
	// as the NodeListView materialized read record (node_list_view key space).
	// The global resolution record (node_resolve) is always written regardless of view.
	Set(ctx context.Context, nv *NodeValue, props map[uuid.UUID]*PropertyValue, view *NodeListView) error
	// Get retrieves a NodeValue by its primary key. Returns nil, nil if not found.
	Get(ctx context.Context, orgID, workspaceID uuid.UUID, nodeType string, nodeID uuid.UUID) (*NodeValue, error)
	// Delete removes a NodeValue and all its property values and secondary indexes.
	Delete(ctx context.Context, nv *NodeValue) error
	// ListByProject returns all nodes of nodeType in a project, ordered by insertion.
	ListByProject(ctx context.Context, orgID, projectID uuid.UUID, nodeType string) ([]*NodeValue, error)
	// ListByState returns all nodes of nodeType in a given state.
	ListByState(ctx context.Context, orgID, workspaceID uuid.UUID, nodeType string, stateID uuid.UUID) ([]*NodeValue, error)
	// ListByProperty returns all nodes where the given property equals pv.
	// Only works for properties that have Indexed=true on their PropertyDef.
	ListByProperty(ctx context.Context, orgID, workspaceID uuid.UUID, nodeType string, propDefID uuid.UUID, pv *PropertyValue) ([]*NodeValue, error)
	// AllocateSequenceID atomically increments and returns the next sequence number
	// for (orgID, projectID, nodeType).
	AllocateSequenceID(ctx context.Context, orgID, projectID uuid.UUID, nodeType string) (int64, error)
	// GetBySequence returns the nodeID for the given sequence number in a project.
	// Returns uuid.Nil, nil when not found.
	GetBySequence(ctx context.Context, orgID, projectID uuid.UUID, nodeType string, seqID int64) (uuid.UUID, error)

	// GetBySlug resolves a global slug index entry: (nodeType, slug) → nodeID.
	// Used for entry-point lookups (e.g. workspace by slug, org by slug).
	GetBySlug(ctx context.Context, nodeType, slug string) (uuid.UUID, error)
	// WriteSlugIndex writes a global slug index entry: (nodeType, slug) → nodeID.
	// Called on create for any node whose NodeType has FeatureHasSlug.
	WriteSlugIndex(ctx context.Context, nodeType, slug string, nodeID uuid.UUID) error
	// DeleteSlugIndex removes a global slug index entry.
	DeleteSlugIndex(ctx context.Context, nodeType, slug string) error

	// CreateAtomic writes a new entity and all related data in a single FDB transaction:
	//   - sequence allocation (if projectID != uuid.Nil)
	//   - NodeValue + secondary indexes
	//   - property values + property indexes
	//   - NodeResolve record (always)
	//   - NodeListView (if view != nil)
	//   - initial assignments (if assigneeIDs non-empty)
	//   - initial labels (if labelIDs non-empty)
	//
	// nv.SequenceID must be 0 on entry; CreateAtomic sets it to the allocated value.
	// If view is non-nil, view.SequenceID is also updated to match.
	// Returns the allocated sequence ID (0 if projectID == uuid.Nil).
	CreateAtomic(
		ctx context.Context,
		orgID, projectID uuid.UUID,
		nv *NodeValue,
		props map[uuid.UUID]*PropertyValue,
		view *NodeListView,
		assigneeIDs []uuid.UUID,
		labelIDs []uuid.UUID,
		actorID uuid.UUID,
	) (sequenceID int64, err error)
}

// AutomationRepository stores and retrieves automation rules.
type AutomationRepository interface {
	Set(ctx context.Context, rule *AutomationRule) error
	Get(ctx context.Context, orgID, ruleID uuid.UUID) (*AutomationRule, error)
	// ListByTrigger returns all enabled rules for a given workspace, node type, and trigger.
	ListByTrigger(ctx context.Context, orgID, workspaceID uuid.UUID, nodeType string, trigger AutomationTrigger) ([]*AutomationRule, error)
	Delete(ctx context.Context, orgID, ruleID uuid.UUID) error
}
