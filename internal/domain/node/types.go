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

// Feature strings are capability declarations on a NodeType. Features drive
// runtime behavior: which properties apply, which relationships are allowed,
// which MCP tools get registered. Features are extensible.
//
// Built-in features shipped by seed:
const (
	FeatureHasSlug                 = "has_slug"                    // instances own a slug / identifier prefix
	FeatureHasWorkflowStates       = "has_workflow_states"         // supports a workflow-state property
	FeatureHasAssignees            = "has_assignees"               // supports assigned_to relationships
	FeatureHasSequenceID           = "has_sequence_id"             // supports a sequence property
	FeatureIsContainer             = "is_container"                // can contain child nodes
	FeatureHasDueDates             = "has_due_dates"               // supports start_date / due_date properties
	FeatureHasComments             = "has_comments"                // supports child comment nodes
	FeatureHasActivity             = "has_activity"                // emits activity nodes on write
	FeatureIsEntryPoint            = "is_entry_point"              // top-level MCP entry (workspace today)
	FeatureIsScope                 = "is_scope"                    // defines an FDB key scope level
	FeatureExcludeFromGenericTools = "exclude_from_generic_tools"  // skip generic CRUD tool registration
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
		{1 << 0, FeatureHasSlug},
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
	// IsBuiltin marks seeded built-in types. Built-in types cannot be deleted via MCP.
	IsBuiltin bool `json:"is_builtin,omitempty"`
	// TypeKey is the stable internal dispatch key used by the seed. Empty for
	// user-defined custom types. Slug and PluralSlug are user-mutable; TypeKey is not.
	TypeKey      string   `json:"type_key,omitempty"`
	Features     Features `json:"features,omitempty"`
	CanContain   []string `json:"can_contain,omitempty"`
	CanLiveUnder []string `json:"can_live_under,omitempty"`
	// DefaultChildren lists nodes that should be auto-created under any new
	// instance of this type. Empty by default: no NodeType implies any
	// automatic child creation unless the seed (or a later edit) declares
	// it explicitly. Used so a freshly created project gets a starter set
	// of states without baking those state names into Go code.
	DefaultChildren []DefaultChild `json:"default_children,omitempty"`
}

// DefaultChild names a child node to auto-create. Props is a JSON object that
// is merged into the child's Props before create.
type DefaultChild struct {
	TypeKey string                     `json:"type_key"`
	Name    string                     `json:"name"`
	Props   map[string]json.RawMessage `json:"props,omitempty"`
}

// HierarchyDepth walks CanLiveUnder to compute how deep a NodeType sits in the
// type DAG. Types with no parents are depth 0.
func HierarchyDepth(nt *NodeType, typeIndex map[string]*NodeType) int {
	if len(nt.CanLiveUnder) == 0 {
		return 0
	}
	maxParentDepth := -1
	for _, parentKey := range nt.CanLiveUnder {
		parent, ok := typeIndex[parentKey]
		if !ok {
			continue
		}
		d := HierarchyDepth(parent, typeIndex)
		if d > maxParentDepth {
			maxParentDepth = d
		}
	}
	return maxParentDepth + 1
}

// BuildTypeIndex creates a lookup map from TypeKey (falling back to Slug) to NodeType.
func BuildTypeIndex(types []*NodeType) map[string]*NodeType {
	m := make(map[string]*NodeType, len(types))
	for _, nt := range types {
		key := nt.TypeKey
		if key == "" {
			key = nt.Slug
		}
		if key != "" {
			m[key] = nt
		}
	}
	return m
}

// Node is the single primary record for every entity in the system. Every
// concept-specific value (workspace, project, state, assignees, labels,
// priority, due dates, description, etc.) lives in Props as JSON-encoded
// property values keyed by PropertyDef name. Everything that connects nodes
// together lives in Relationship records, not on the node itself.
//
// The universal-fields rule for Node: a field earns a spot on the struct only
// if it would exist on a node in a completely different product (CMS, CRM,
// game engine) built on the same architecture.
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

// Relationship is a generic directed edge between two nodes. The RelationType
// string carries the semantics: "assigned_to", "labeled_with", "child_of",
// "watches", "mentioned_in", "reacted_to", "comment_of", etc.
//
// Relationships replace all dedicated concept-specific join records: assignments,
// labels-on-nodes, parent-child containment, watchers, mentions, reactions.
// New relation types are user-extensible; nothing in the storage layer privileges
// one RelationType over another.
type Relationship struct {
	OrgID        uuid.UUID                  `json:"org_id"`
	SourceID     uuid.UUID                  `json:"source_id"`
	RelationType string                     `json:"relation_type"`
	TargetID     uuid.UUID                  `json:"target_id"`
	CreatedBy    uuid.UUID                  `json:"created_by"`
	CreatedAt    time.Time                  `json:"created_at"`
	Props        map[string]json.RawMessage `json:"props,omitempty"`
}

// Well-known relation types seeded by default. Nothing stops callers from
// defining their own; these exist so built-in features wire to consistent strings.
const (
	RelAssignedTo    = "assigned_to"
	RelLabeledWith   = "labeled_with"
	RelChildOf       = "child_of"
	RelWatches       = "watches"
	RelMentionedIn   = "mentioned_in"
	RelReactedTo     = "reacted_to"
	RelCommentOf     = "comment_of"
	RelActivityOn    = "activity_on"
	RelActorOf       = "actor_of"
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

// PropertyDef defines a property that can be attached to one or more node types.
// A PropertyDef applies to a NodeType when AppliesToFeatures is empty (applies
// to all) or when the NodeType declares any of the listed features. PropertyDefs
// are never scoped by hardcoded NodeType name lists.
type PropertyDef struct {
	ID    uuid.UUID    `json:"id"`
	OrgID uuid.UUID    `json:"org_id"`
	Name  string       `json:"name"`
	Type  PropertyType `json:"type"`
	// AppliesToFeatures scopes this PropertyDef to NodeTypes that declare any
	// of these features. Empty means applies to every NodeType.
	AppliesToFeatures []string `json:"applies_to_features,omitempty"`
	// Indexed controls whether FDB maintains a secondary sort index on this property.
	Indexed      bool            `json:"indexed"`
	Options      []EnumOption    `json:"options,omitempty"` // for select / multi_select
	Required     bool            `json:"required"`
	DefaultValue json.RawMessage `json:"default_value,omitempty"`
}

// Deterministic-ID namespaces. Distinct per layer so a slug collision in one
// layer cannot ever produce the same UUID as the slug in another layer.
var (
	orgNamespace       = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000001")
	workspaceNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000002")
	systemPropNS       = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
)

// OrgID returns the deterministic UUID for an org node identified by slug. A
// fresh FDB seeded with the same slug produces byte-identical IDs across runs,
// which means NodeType and PropertyDef IDs (which derive from orgID) are also
// stable across wipes.
func OrgID(slug string) uuid.UUID {
	return uuid.NewSHA1(orgNamespace, []byte(slug))
}

// WorkspaceID returns the deterministic UUID for a workspace node under a
// given org, identified by slug.
func WorkspaceID(orgID uuid.UUID, slug string) uuid.UUID {
	return uuid.NewSHA1(workspaceNamespace, []byte(orgID.String()+":"+slug))
}

// SystemPropID returns a deterministic UUID for a built-in property definition
// scoped to an org. Used by seed and migration scripts to compute stable IDs
// without FDB reads.
func SystemPropID(orgID uuid.UUID, propName string) uuid.UUID {
	return uuid.NewSHA1(systemPropNS, []byte(orgID.String()+":"+propName))
}
