package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Op enumerates the operations that can be enabled on a node type.
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

// Feature strings declare extensible capabilities on a NodeType. Runtime code
// uses features to select properties, relationships, and MCP tools.
const (
	FeatureHasWorkflowStates       = "has_workflow_states"        // supports a workflow-state property
	FeatureHasAssignees            = "has_assignees"              // supports assigned_to relationships
	FeatureHasSequenceID           = "has_sequence_id"            // supports a sequence property
	FeatureIsContainer             = "is_container"               // can contain child nodes
	FeatureHasDueDates             = "has_due_dates"              // supports start_date / due_date properties
	FeatureHasComments             = "has_comments"               // supports child comment nodes
	FeatureHasActivity             = "has_activity"               // emits activity nodes on write
	FeatureIsEntryPoint            = "is_entry_point"             // top-level MCP entry (workspace today)
	FeatureIsScope                 = "is_scope"                   // defines an FDB key scope level
	FeatureExcludeFromGenericTools = "exclude_from_generic_tools" // skip generic CRUD tool registration
)

// Features is a set of capability strings on a NodeType.
type Features []string

// Has reports whether the feature set includes the given feature.
func (f Features) Has(feat string) bool {
	for _, s := range f {
		if s == feat {
			return true
		}
	}

	return false
}

// HasAny reports whether the feature set includes any of the given features.
func (f Features) HasAny(feats ...string) bool {
	for _, want := range feats {
		if f.Has(want) {
			return true
		}
	}

	return false
}

// UnmarshalJSON handles the legacy uint32 bitmask and the current []string format.
// The bitmask support exists to read pre-migration FDB records; new writes always
// produce []string.
func (f *Features) UnmarshalJSON(b []byte) error {
	var ss []string
	if err := json.Unmarshal(b, &ss); err == nil {
		*f = ss
		return nil
	}

	var bits uint32
	if err := json.Unmarshal(b, &bits); err != nil {
		return err
	}

	mapping := []struct {
		bit  uint32
		name string
	}{
		{1 << 0, FeatureHasAddress},
		{1 << 1, FeatureHasWorkflowStates},
		{1 << 2, FeatureHasAssignees},
		{1 << 3, FeatureHasSequenceID},
		{1 << 4, FeatureIsContainer},
		{1 << 5, FeatureHasDueDates},
		{1 << 6, FeatureHasComments},
		{1 << 7, FeatureHasActivity},
		{1 << 8, FeatureIsEntryPoint},
		{1 << 9, FeatureIsScope},
		{1 << 10, FeatureExcludeFromGenericTools},
	}
	var out Features

	for _, m := range mapping {
		if bits&m.bit != 0 {
			out = append(out, m.name)
		}
	}

	*f = out

	return nil
}

// ReferenceStrategy selects how a NodeType derives human-readable references
// for display and MCP input. UUID remains the canonical node identity.
type ReferenceStrategy string

const (
	ReferenceUUIDOnly       ReferenceStrategy = "uuid_only"
	ReferenceScopedSequence ReferenceStrategy = "scoped_sequence"
	ReferenceScopedProperty ReferenceStrategy = "scoped_property"
)

// ReferenceConfig defines a NodeType's human-readable reference contract.
// Property specifies the canonical property when Strategy uses property lookup.
type ReferenceConfig struct {
	Strategy ReferenceStrategy `json:"strategy"`
	Property string            `json:"property,omitempty"`
}

// NodeType defines a type in the extensibility hierarchy. NodeType records are
// themselves nodes of NodeType "node_type" in the same FDB key space as any
// other node. This is configuration metadata, not runtime data.
type NodeType struct {
	ID             uuid.UUID   `json:"id"`
	OrgID          uuid.UUID   `json:"org_id"`
	Name           string      `json:"name"`
	Slug           string      `json:"slug"`
	Color          string      `json:"color"`
	Icon           string      `json:"icon"`
	AllowedOps     []Op        `json:"allowed_ops"`
	PropertyDefIDs []uuid.UUID `json:"property_def_ids"`
	PluralSlug     string      `json:"plural_slug,omitempty"`
	// IsBuiltin marks seeded types that MCP tools cannot delete.
	IsBuiltin bool `json:"is_builtin,omitempty"`
	// TypeKey is the stable internal dispatch key. Users can edit Slug and
	// PluralSlug for display and tool names, but cannot edit TypeKey.
	TypeKey      string   `json:"type_key,omitempty"`
	Features     Features `json:"features,omitempty"`
	CanContain   []string `json:"can_contain,omitempty"`
	CanLiveUnder []string `json:"can_live_under,omitempty"`
	// Reference declares how humans and MCP callers can refer to nodes of this
	// type without using UUIDs. Runtime code interprets the strategy without
	// branching on concrete slugs or built-in type names.
	Reference ReferenceConfig `json:"reference,omitempty"`
	// ReferenceTemplates declare the rendered references this type owns.
	ReferenceTemplates []ReferenceTemplate `json:"reference_templates,omitempty"`
	// DefaultChildren specifies nodes to create automatically under new instances.
	// An empty list disables automatic child creation. Seed metadata can supply
	// starter states for projects without hardcoded state names in runtime code.
	DefaultChildren []DefaultChild `json:"default_children,omitempty"`
}

// DefaultChild specifies a child to create automatically. Creation merges
// its Props into the child's properties.
type DefaultChild struct {
	TypeKey string                     `json:"type_key"`
	Name    string                     `json:"name"`
	Props   map[string]json.RawMessage `json:"props,omitempty"`
}

// BuildTypeIndex creates a lookup map from TypeKey to NodeType.
func BuildTypeIndex(types []*NodeType) map[string]*NodeType {
	m := make(map[string]*NodeType, len(types))
	for _, nt := range types {
		if nt.TypeKey != "" {
			m[nt.TypeKey] = nt
		}
	}

	return m
}

// PrimaryReferenceTemplate returns this type's human-readable reference template.
func (n *NodeType) PrimaryReferenceTemplate() *ReferenceTemplate {
	for index := range n.ReferenceTemplates {
		if n.ReferenceTemplates[index].IsPrimary {
			return &n.ReferenceTemplates[index]
		}
	}

	return nil
}

// Node stores the primary record for every entity. Props stores product-specific
// values as JSON keyed by PropertyDef name. Relationship records connect nodes.
// Universal struct fields must also apply to nodes in other products using
// this architecture, such as a CMS, CRM, or game engine.
type Node struct {
	ID        uuid.UUID                  `json:"id"`
	OrgID     uuid.UUID                  `json:"org_id"`
	NodeType  string                     `json:"node_type"`
	Name      string                     `json:"name"`
	Props     map[string]json.RawMessage `json:"props,omitempty"`
	CreatedBy uuid.UUID                  `json:"created_by"`
	UpdatedBy uuid.UUID                  `json:"updated_by"`
	CreatedAt time.Time                  `json:"created_at"`
	UpdatedAt time.Time                  `json:"updated_at"`
}

// Relationship is a directed edge between nodes. RelationType specifies its
// meaning, such as assigned_to, labeled_with, or child_of. Relationships replace
// dedicated join records for assignments, labels, hierarchy, and reactions.
// Users can define relation types without storage-layer special cases.
type Relationship struct {
	OrgID        uuid.UUID                  `json:"org_id"`
	SourceID     uuid.UUID                  `json:"source_id"`
	RelationType string                     `json:"relation_type"`
	TargetID     uuid.UUID                  `json:"target_id"`
	CreatedBy    uuid.UUID                  `json:"created_by"`
	CreatedAt    time.Time                  `json:"created_at"`
	Props        map[string]json.RawMessage `json:"props,omitempty"`
}

// Built-in features use these seeded relation types. Callers can define others.
const (
	RelAssignedTo  = "assigned_to"
	RelLabeledWith = "labeled_with"
	RelChildOf     = "child_of"
	RelWatches     = "watches"
	RelMentionedIn = "mentioned_in"
	RelReactedTo   = "reacted_to"
	RelCommentOf   = "comment_of"
	RelActivityOn  = "activity_on"
	RelActorOf     = "actor_of"
)

// PropertyType enumerates value shapes a PropertyDef can declare.
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
	PropertyTypeUUID        PropertyType = "uuid"
)

// EnumOption is one option in a select or multi-select property.
type EnumOption struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Color    string `json:"color,omitempty"`
	SortRank int32  `json:"sort_rank"`
}

// PropertyDef defines a property for node types. It applies to all types when
// AppliesToFeatures is empty and otherwise to types with any listed feature.
// Property definitions do not depend on hardcoded type-name lists.
type PropertyDef struct {
	ID    uuid.UUID    `json:"id"`
	OrgID uuid.UUID    `json:"org_id"`
	Name  string       `json:"name"`
	Type  PropertyType `json:"type"`
	// AppliesToFeatures selects NodeTypes declaring any listed feature.
	// An empty list applies to every NodeType.
	AppliesToFeatures []string `json:"applies_to_features,omitempty"`
	// Indexed controls whether FDB maintains a secondary sort index on this property.
	Indexed  bool         `json:"indexed"`
	Options  []EnumOption `json:"options,omitempty"` // for select / multi_select
	Required bool         `json:"required"`
	// DefaultValue stores a literal JSON default for non-reference property values.
	DefaultValue json.RawMessage `json:"default_value,omitempty"`
	// DefaultReference stores a human-reference default resolved through the
	// target NodeType Reference contract for UUID-valued properties.
	DefaultReference *PropertyDefaultReference `json:"default_reference,omitempty"`
	// ReferenceTargetTypeKey specifies the NodeType that resolves human references
	// for UUID-valued properties. An empty value treats UUIDs as opaque.
	ReferenceTargetTypeKey string `json:"reference_target_type_key,omitempty"`
	// Search declares the explicit text projection for this property. A nil
	// value is incomplete metadata and blocks the initial search rebuild.
	Search *SearchProjection `json:"search"`
}

// These namespaces make workspace and system-property IDs reproducible from
// stable org and property or address inputs across resets.
var (
	workspaceNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000002")
	systemPropNS       = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
)

// NewOrgID returns a fresh UUIDv7 independently of the org's slug or address.
// Tenants sharing a slug receive different IDs and FDB key prefixes (TACK-230).
func NewOrgID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// WorkspaceID returns the deterministic UUID for a workspace node under a
// given org, identified by address.
func WorkspaceID(orgID uuid.UUID, address string) uuid.UUID {
	return uuid.NewSHA1(workspaceNamespace, []byte(orgID.String()+":"+address))
}

// SystemPropID derives an org-specific UUID for a built-in property definition.
// Seed and migrations use it to compute stable IDs without FDB reads.
func SystemPropID(orgID uuid.UUID, propName string) uuid.UUID {
	return uuid.NewSHA1(systemPropNS, []byte(orgID.String()+":"+propName))
}
