package node

import (
	"context"

	"github.com/google/uuid"
)

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
// Replaces SQL issue_assignees and epic_assignees join tables.
type AssignmentRepository interface {
	// SetAll replaces all assignees on a node atomically.
	SetAll(ctx context.Context, orgID, nodeID uuid.UUID, userIDs []uuid.UUID, assignedBy uuid.UUID) error
	// ListByNode returns all userIDs assigned to a node.
	ListByNode(ctx context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error)
	// ListByUser returns all nodeIDs assigned to a user.
	ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]uuid.UUID, error)
}

// NodeLabelRepository manages labels on any node type.
// Replaces SQL issue_labels and epic_labels join tables.
type NodeLabelRepository interface {
	// SetAll replaces all labels on a node atomically.
	SetAll(ctx context.Context, orgID, nodeID uuid.UUID, labelIDs []uuid.UUID, addedBy uuid.UUID) error
	// ListByNode returns all labelIDs on a node.
	ListByNode(ctx context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error)
	// ListByLabel returns all nodeIDs with a given label.
	ListByLabel(ctx context.Context, orgID, labelID uuid.UUID) ([]uuid.UUID, error)
}

// ContainmentRepository manages module↔issue and cycle↔issue membership.
// Replaces SQL module_issues and cycle_issues join tables.
type ContainmentRepository interface {
	AddIssueToModule(ctx context.Context, orgID, moduleID, issueID, addedBy uuid.UUID) error
	RemoveIssueFromModule(ctx context.Context, orgID, moduleID, issueID uuid.UUID) error
	IssuesInModule(ctx context.Context, orgID, moduleID uuid.UUID) ([]uuid.UUID, error)
	ModulesContainingIssue(ctx context.Context, orgID, issueID uuid.UUID) ([]uuid.UUID, error)

	AddIssueToCycle(ctx context.Context, orgID, cycleID, issueID, addedBy uuid.UUID) error
	RemoveIssueFromCycle(ctx context.Context, orgID, cycleID, issueID uuid.UUID) error
	IssuesInCycle(ctx context.Context, orgID, cycleID uuid.UUID) ([]uuid.UUID, error)
	CyclesContainingIssue(ctx context.Context, orgID, issueID uuid.UUID) ([]uuid.UUID, error)
}
