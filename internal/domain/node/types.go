package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Op enumerates the operations that can be enabled on a custom node type.
type Op string

const (
	OpCreate Op = "create"
	OpRead   Op = "read"
	OpList   Op = "list"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
)

// AllOps is the default set of allowed operations for a new node type.
var AllOps = []Op{OpCreate, OpRead, OpList, OpUpdate, OpDelete}

// NodeType defines a user-defined type in the extensibility hierarchy.
type NodeType struct {
	ID             uuid.UUID   `json:"id"`
	OrgID          uuid.UUID   `json:"org_id"`
	Name           string      `json:"name"`
	Slug           string      `json:"slug"`           // kebab-case, used in tool names
	Color          string      `json:"color"`
	Icon           string      `json:"icon"`
	CanContain     []string    `json:"can_contain"`    // names of types this type may parent
	CanLiveUnder   []string    `json:"can_live_under"` // names of types this type may be child of
	AllowedOps     []Op        `json:"allowed_ops"`    // which MCP tools are generated
	PropertyDefIDs []uuid.UUID `json:"property_def_ids"`
}

// PropertyType enumerates supported value types for custom properties.
type PropertyType string

const (
	PropertyTypeText        PropertyType = "text"
	PropertyTypeNumber      PropertyType = "number"
	PropertyTypeDate        PropertyType = "date"
	PropertyTypeSelect      PropertyType = "select"
	PropertyTypeMultiSelect PropertyType = "multi_select"
	PropertyTypeURL         PropertyType = "url"
	PropertyTypeCheckbox    PropertyType = "checkbox"
)

// PropertyDef defines a custom property at workspace or project scope.
type PropertyDef struct {
	ID           uuid.UUID    `json:"id"`
	OrgID        uuid.UUID    `json:"org_id"`
	WorkspaceID  *uuid.UUID   `json:"workspace_id,omitempty"`
	ProjectID    *uuid.UUID   `json:"project_id,omitempty"`
	Name         string       `json:"name"`
	Type         PropertyType `json:"type"`
	Options      []string     `json:"options,omitempty"` // for select / multi_select
	Required     bool         `json:"required"`
	DefaultValue any          `json:"default_value,omitempty"`
}

// ActivityEvent is an immutable record of a change to a node.
type ActivityEvent struct {
	EventID   uuid.UUID      `json:"event_id"`
	NodeID    uuid.UUID      `json:"node_id"`
	Actor     uuid.UUID      `json:"actor"`
	Verb      string         `json:"verb"`   // e.g. "created", "state_changed"
	Detail    map[string]any `json:"detail"` // verb-specific payload
	CreatedAt time.Time      `json:"created_at"`
}

// Properties is a resolved property bag for a node: def UUID → raw JSON value.
type Properties map[uuid.UUID]json.RawMessage
