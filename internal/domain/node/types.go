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

// Feature strings declare NodeType capabilities. Features control applicable
// properties, allowed relationships, and MCP tool registration. Additional
// features can extend these declarations. The seed supplies these built-in
// features.
const (
	FeatureHasWorkflowStates       = "has_workflow_states"        // This feature supports a workflow-state property.
	FeatureHasAssignees            = "has_assignees"              // This feature supports assigned_to relationships.
	FeatureHasSequenceID           = "has_sequence_id"            // This feature supports a sequence property.
	FeatureIsContainer             = "is_container"               // This feature allows child nodes.
	FeatureHasDueDates             = "has_due_dates"              // It supports start_date and due_date properties.
	FeatureHasComments             = "has_comments"               // This feature supports child comment nodes.
	FeatureHasActivity             = "has_activity"               // This feature emits activity nodes on writes.
	FeatureIsEntryPoint            = "is_entry_point"             // It marks top-level MCP entry points; workspace has it.
	FeatureIsScope                 = "is_scope"                   // This feature defines a FoundationDB key scope level.
	FeatureExcludeFromGenericTools = "exclude_from_generic_tools" // This feature excludes generic CRUD tool registration.
)

// Features is a set of capability strings on a NodeType.
type Features []string

// Has reports whether the feature set includes feat.
//
// The method compares each feature string with feat.
//
//   - An exact match returns true.
//   - The method returns false when every comparison differs.
func (f Features) Has(feat string) bool {
	for _, s := range f {
		if s == feat {
			return true
		}
	}
	return false
}

// HasAny reports whether the feature set includes any supplied feature.
//
// The method checks each argument with [Features.Has].
//
//   - The first matching feature returns true.
//   - An empty argument list or a list without a match returns false.
func (f Features) HasAny(feats ...string) bool {
	for _, want := range feats {
		if f.Has(want) {
			return true
		}
	}
	return false
}

// UnmarshalJSON reads a string array or a legacy uint32 feature bitmask.
//
// Bitmask support permits reads of existing records from before the format
// migration. New writes use the string array format.
//
//   - The method first attempts to decode a string array.
//   - A bitmask maps each recognized bit to its feature string.
//   - Invalid input returns the uint32 decoding error.
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

// ReferenceStrategy selects how a NodeType derives human-readable
// references for display and MCP input. UUID remains the canonical node
// identity.
type ReferenceStrategy string

const (
	ReferenceUUIDOnly       ReferenceStrategy = "uuid_only"
	ReferenceScopedSequence ReferenceStrategy = "scoped_sequence"
	ReferenceScopedProperty ReferenceStrategy = "scoped_property"
)

// ReferenceConfig declares the human-readable reference contract for a
// NodeType. Property selects the canonical property when Strategy uses
// property lookup.
type ReferenceConfig struct {
	Strategy ReferenceStrategy `json:"strategy"`
	Property string            `json:"property,omitempty"`
}

// NodeType defines a type in the extensibility hierarchy. Each NodeType
// record is a node of type "node_type" in the shared FoundationDB key space.
// These records store configuration metadata.
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
	// IsBuiltin marks seeded built-in types. MCP cannot delete these types.
	IsBuiltin bool `json:"is_builtin,omitempty"`
	// TypeKey is the stable internal dispatch key. Users can change Slug and
	// PluralSlug for display and tool registration, but cannot change TypeKey.
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
	// DefaultChildren declares the child nodes to create under each new
	// instance. The default is empty. Only an explicit seed or later edit
	// enables automatic child creation. A project can declare starter states
	// without hardcoded state names in Go.
	DefaultChildren []DefaultChild `json:"default_children,omitempty"`
}

// DefaultChild specifies a child node to create automatically. Creation
// merges this JSON Props object into the child's Props.
type DefaultChild struct {
	TypeKey string                     `json:"type_key"`
	Name    string                     `json:"name"`
	Props   map[string]json.RawMessage `json:"props,omitempty"`
}

// BuildTypeIndex maps each nonempty TypeKey to its NodeType.
//
// The function examines the supplied types in order.
//
//   - An empty TypeKey does not create an entry.
//   - A later type replaces an earlier entry with the same TypeKey.
func BuildTypeIndex(types []*NodeType) map[string]*NodeType {
	m := make(map[string]*NodeType, len(types))
	for _, nt := range types {
		if nt.TypeKey != "" {
			m[nt.TypeKey] = nt
		}
	}
	return m
}

// PrimaryReferenceTemplate returns the type's primary human-readable
// reference template.
//
// The method searches ReferenceTemplates in slice order.
//
//   - The first template with IsPrimary returns a pointer to that entry.
//   - The method returns nil when every template lacks IsPrimary.
func (n *NodeType) PrimaryReferenceTemplate() *ReferenceTemplate {
	for index := range n.ReferenceTemplates {
		if n.ReferenceTemplates[index].IsPrimary {
			return &n.ReferenceTemplates[index]
		}
	}
	return nil
}

// Node is the primary record for every entity. Props stores concept-specific
// JSON property values under PropertyDef names, including workspace,
// project, state, assignees, labels, priority, due dates, and description.
// Relationship records store connections between nodes.
//
// Add a field to Node only if that field would exist in a different product,
// such as a CMS, CRM, or game engine, on this architecture.
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

// Relationship is a directed edge between two nodes. RelationType selects
// the edge's meaning, including "assigned_to", "labeled_with", "child_of",
// "watches", "mentioned_in", "reacted_to", and "comment_of".
//
// Relationship records represent assignments, labels on nodes, parent-child
// containment, watchers, mentions, and reactions. Users can extend relation
// types. The storage layer must treat every RelationType equally.
type Relationship struct {
	OrgID        uuid.UUID                  `json:"org_id"`
	SourceID     uuid.UUID                  `json:"source_id"`
	RelationType string                     `json:"relation_type"`
	TargetID     uuid.UUID                  `json:"target_id"`
	CreatedBy    uuid.UUID                  `json:"created_by"`
	CreatedAt    time.Time                  `json:"created_at"`
	Props        map[string]json.RawMessage `json:"props,omitempty"`
}

// The seed supplies these relation types for consistent built-in feature
// behavior. Callers can also define relation types.
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

// PropertyDef defines a property for one or more node types. It applies to
// every NodeType when AppliesToFeatures is empty, or to a NodeType that
// declares any listed feature. PropertyDefs must not use hardcoded NodeType
// name lists for scope.
type PropertyDef struct {
	ID    uuid.UUID    `json:"id"`
	OrgID uuid.UUID    `json:"org_id"`
	Name  string       `json:"name"`
	Type  PropertyType `json:"type"`
	// AppliesToFeatures limits the property to NodeTypes that declare any listed
	// feature. An empty list applies to every NodeType.
	AppliesToFeatures []string `json:"applies_to_features,omitempty"`
	// Indexed controls whether FoundationDB maintains a secondary sort index
	// for this property.
	Indexed  bool         `json:"indexed"`
	Options  []EnumOption `json:"options,omitempty"` // Options supplies select and multi_select choices.
	Required bool         `json:"required"`
	// DefaultValue stores a literal JSON default for non-reference property values.
	DefaultValue json.RawMessage `json:"default_value,omitempty"`
	// DefaultReference stores a human-reference default resolved through the
	// target NodeType Reference contract for UUID-valued properties.
	DefaultReference *PropertyDefaultReference `json:"default_reference,omitempty"`
	// ReferenceTargetTypeKey selects the NodeType that resolves human
	// references for UUID-valued properties. An empty value treats the UUID as
	// opaque.
	ReferenceTargetTypeKey string `json:"reference_target_type_key,omitempty"`
	// Search declares the explicit text projection for this property. A nil
	// value selects the effective type default. Readiness rejects invalid
	// declarations and definitions without a valid declaration or default
	// before rebuilding.
	Search *SearchProjection `json:"search"`
}

// The workspace and system-property namespaces produce deterministic IDs.
// WorkspaceID uses orgID and address; SystemPropID uses orgID and propName.
// Callers rely on these stable inputs to reproduce IDs across wipes.
// TACK-230 removed orgNamespace.
var (
	workspaceNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000002")
	systemPropNS       = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
)

// NewOrgID returns a fresh UUIDv7 for a new organization.
//
// The ID is random and opaque. The slug or address does not determine it.
// The original incident account attributes the 2026-05-09 parallel-org
// outage to SHA-1 organization ID derivation. TACK-230 replaced that
// derivation.
//
//   - Tenants with the same slug receive different organization IDs.
//   - Each organization uses a different FoundationDB key prefix.
func NewOrgID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// WorkspaceID returns a deterministic workspace UUID within an organization.
//
// The organization ID and workspace address determine the result.
//
//   - Repeated calls with the same inputs return the same UUID.
//   - The function does not read FoundationDB.
func WorkspaceID(orgID uuid.UUID, address string) uuid.UUID {
	return uuid.NewSHA1(workspaceNamespace, []byte(orgID.String()+":"+address))
}

// SystemPropID returns a deterministic built-in property definition UUID.
//
// The organization ID and property name determine the result. Seed and
// migration callers use it to calculate stable IDs without FoundationDB reads.
//
//   - Repeated calls with the same inputs return the same UUID.
func SystemPropID(orgID uuid.UUID, propName string) uuid.UUID {
	return uuid.NewSHA1(systemPropNS, []byte(orgID.String()+":"+propName))
}
