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
