package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Built-in node type slugs. Custom types use user-defined slugs stored in FDB.
const (
	NodeTypeIssue       = "issue"
	NodeTypeEpic        = "epic"
	NodeTypeCycle       = "cycle"
	NodeTypeModule      = "module"
	NodeTypeOrg         = "org"
	NodeTypeWorkspace   = "workspace"
	NodeTypeProject     = "project"
	NodeTypeState       = "state"
	NodeTypeLabel       = "label"
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

// NodeFeatures is a bitset of capabilities for a NodeType.
// Behavior is driven by Features, not TypeKey.
type NodeFeatures uint32

const (
	FeatureHasSlug           NodeFeatures = 1 << 0 // instances own an identifier prefix e.g. "ENG"
	FeatureHasWorkflowStates NodeFeatures = 1 << 1 // instances have StateID, can transition
	FeatureHasAssignees      NodeFeatures = 1 << 2 // instances can be assigned to users
	FeatureHasSequenceID     NodeFeatures = 1 << 3 // instances get {parent_slug}-{N} identifier
	FeatureIsContainer       NodeFeatures = 1 << 4 // instances can contain child nodes
	FeatureHasDueDates       NodeFeatures = 1 << 5 // instances have start/end date system props
	FeatureHasComments       NodeFeatures = 1 << 6 // instances support threaded comments
	FeatureHasActivity       NodeFeatures = 1 << 7 // instances emit activity events on write
)

// Has reports whether f includes feat.
func (f NodeFeatures) Has(feat NodeFeatures) bool { return f&feat != 0 }

// NodeType defines a user-defined type in the extensibility hierarchy.
type NodeType struct {
	ID             uuid.UUID   `json:"id"`
	OrgID          uuid.UUID   `json:"org_id"`
	Name           string      `json:"name"`
	Slug           string      `json:"slug"`           // kebab-case, used in tool names
	Color          string      `json:"color"`
	Icon           string      `json:"icon"`
	AllowedOps     []Op        `json:"allowed_ops"`    // which MCP tools are generated
	PropertyDefIDs []uuid.UUID `json:"property_def_ids"`
	// PluralSlug is used for list tool names: "tack_list_{plural_slug}".
	// If empty, the tool registration falls back to Slug + "s".
	PluralSlug string `json:"plural_slug,omitempty"`
	// IsBuiltin marks seeded built-in types (issue, epic, cycle, module).
	// Built-in types cannot be deleted via MCP.
	IsBuiltin bool `json:"is_builtin,omitempty"`
	// TypeKey is the stable internal dispatch key: "issue", "epic", "cycle", "module".
	// Empty for user-defined custom types. Slug and PluralSlug are user-mutable;
	// TypeKey is not.
	TypeKey      string       `json:"type_key,omitempty"`
	Features     NodeFeatures `json:"features"`
	CanContain   []string     `json:"can_contain,omitempty"`    // TypeKeys this type can parent
	CanLiveUnder []string     `json:"can_live_under,omitempty"` // TypeKeys this type can be a child of
}

// NodeValue is the lean core stored in FDB for every node.
// Properties (priority, due_date, etc.) live separately as PropertyValues
// and are managed by EntityRepository alongside the NodeValue.
type NodeValue struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   uuid.UUID  `json:"project_id"`
	NodeType    string     `json:"node_type"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	StateID     *uuid.UUID `json:"state_id,omitempty"`
	ParentID    *uuid.UUID `json:"parent_id,omitempty"`
	// EpicID links an issue to its parent epic. Epic membership is one-to-one
	// (an issue belongs to at most one epic), so it lives on NodeValue rather
	// than as a PropertyValue or containment relation.
	EpicID     *uuid.UUID `json:"epic_id,omitempty"`
	SequenceID  int32      `json:"sequence_id"`
	SortOrder   float64    `json:"sort_order"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	UpdatedBy   uuid.UUID  `json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// PropertyValueKind enumerates the supported value types for properties.
type PropertyValueKind string

const (
	PropertyValueText      PropertyValueKind = "text"
	PropertyValueInt       PropertyValueKind = "int"
	PropertyValueFloat     PropertyValueKind = "float"
	PropertyValueBool      PropertyValueKind = "bool"
	PropertyValueTimestamp PropertyValueKind = "timestamp"
	// PropertyValueEnum references a key from the PropertyDef's EnumOptions list.
	PropertyValueEnum PropertyValueKind = "enum"
)

// PropertyValue holds a typed value for a single property on a node.
// Only the field matching Kind is populated; all others are nil.
type PropertyValue struct {
	Kind      PropertyValueKind `json:"kind"`
	Text      *string           `json:"text,omitempty"`
	Int       *int64            `json:"int,omitempty"`
	Float     *float64          `json:"float,omitempty"`
	Bool      *bool             `json:"bool,omitempty"`
	Timestamp *time.Time        `json:"timestamp,omitempty"`
	Enum      *string           `json:"enum,omitempty"`
	// EnumRank is the SortRank of the selected EnumOption, populated by the
	// service layer so the FDB secondary index sorts by rank without a
	// PropertyDef lookup inside the write transaction.
	EnumRank *int32 `json:"enum_rank,omitempty"`
}

// EnumOption is one option in a select or multi-select property.
type EnumOption struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Color    string `json:"color,omitempty"`
	// SortRank determines the ordering of this option in FDB secondary index scans.
	// Lower rank = earlier in sort order. Assigned by the service on creation.
	SortRank int32 `json:"sort_rank"`
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
	PropertyTypeTimestamp   PropertyType = "timestamp"
)

// PropertyDef defines a custom property that can be attached to one or more node types.
type PropertyDef struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	WorkspaceID *uuid.UUID `json:"workspace_id,omitempty"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	Name        string     `json:"name"`
	Type        PropertyType `json:"type"`
	// NodeTypes lists the node type slugs this property applies to.
	// Empty means it applies to all node types in the workspace.
	NodeTypes []string `json:"node_types,omitempty"`
	// Indexed controls whether FDB maintains a secondary sort index on this
	// property, enabling efficient filtered listing and ordering.
	Indexed bool `json:"indexed"`
	// IsSystem marks seeded defaults (priority, due_date, etc.) that ship
	// pre-installed on every new workspace. Users may edit but not delete them.
	IsSystem     bool           `json:"is_system"`
	Options      []EnumOption   `json:"options,omitempty"` // for select / multi_select
	Required     bool           `json:"required"`
	DefaultValue *PropertyValue `json:"default_value,omitempty"`
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

// Properties is a resolved property bag for a node: def UUID to raw JSON value.
// Used by PropertyRepository.GetValues; new code should prefer typed PropertyValue.
type Properties map[uuid.UUID]json.RawMessage

// AutomationTrigger identifies the event that fires an automation rule.
type AutomationTrigger string

const (
	TriggerNodeCreated     AutomationTrigger = "node.created"
	TriggerNodeUpdated     AutomationTrigger = "node.updated"
	TriggerStateChanged    AutomationTrigger = "node.state_changed"
	TriggerPropertyChanged AutomationTrigger = "node.property_changed"
	TriggerNodeDeleted     AutomationTrigger = "node.deleted"
)

// SetPropertyAction sets a property value on the triggering node.
type SetPropertyAction struct {
	PropertyDefID uuid.UUID      `json:"property_def_id"`
	Value         *PropertyValue `json:"value"`
}

// SetStateAction transitions the triggering node to a new state.
type SetStateAction struct {
	StateID uuid.UUID `json:"state_id"`
}

// AutomationRule is a stored if-trigger-then-action rule scoped to a workspace.
// Exactly one of SetProperty or SetState is non-nil.
type AutomationRule struct {
	ID          uuid.UUID         `json:"id"`
	OrgID       uuid.UUID         `json:"org_id"`
	WorkspaceID uuid.UUID         `json:"workspace_id"`
	// NodeType scopes the rule to one node type slug; empty means all types.
	NodeType string            `json:"node_type,omitempty"`
	Trigger  AutomationTrigger `json:"trigger"`
	// TriggerPropDefID is set only when Trigger == TriggerPropertyChanged.
	TriggerPropDefID *uuid.UUID `json:"trigger_prop_def_id,omitempty"`
	Enabled          bool       `json:"enabled"`
	// Actions: exactly one is non-nil.
	SetProperty *SetPropertyAction `json:"set_property,omitempty"`
	SetState    *SetStateAction    `json:"set_state,omitempty"`
}
