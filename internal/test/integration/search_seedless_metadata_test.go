package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

const (
	seedlessParentProperty = "parent_id"
	seedlessScopeProperty  = "scope_id"
	firstOptionLabel       = "Willow ember"
	secondOptionLabel      = "Quartz orchid"
)

type seedlessKeys struct {
	Root, Entry, Container, Nested               string
	Address, Included, Excluded, Default, Choice string
	FirstOption, SecondOption                    string
}

func newSeedlessKeys() seedlessKeys {
	return seedlessKeys{
		Root: opaqueSeedlessKey("t"), Entry: opaqueSeedlessKey("t"),
		Container: opaqueSeedlessKey("t"), Nested: opaqueSeedlessKey("t"),
		Address: opaqueSeedlessKey("p"), Included: opaqueSeedlessKey("p"), Excluded: opaqueSeedlessKey("p"),
		Default: opaqueSeedlessKey("p"), Choice: opaqueSeedlessKey("p"),
		FirstOption: opaqueSeedlessKey("o"), SecondOption: opaqueSeedlessKey("o"),
	}
}

func (k seedlessKeys) all() []string {
	return []string{
		k.Root, k.Entry, k.Container, k.Nested, k.Address, k.Included, k.Excluded,
		k.Default, k.Choice, k.FirstOption, k.SecondOption,
	}
}

func opaqueSeedlessKey(label string) string {
	return label + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

type seedlessDefinitions struct {
	Address, Parent, Scope              *node.PropertyDef
	Included, Excluded, Default, Choice *node.PropertyDef
}

func (d seedlessDefinitions) all() []*node.PropertyDef {
	return []*node.PropertyDef{d.Address, d.Parent, d.Scope, d.Included, d.Excluded, d.Default, d.Choice}
}

func (d seedlessDefinitions) content() []uuid.UUID {
	return []uuid.UUID{d.Included.ID, d.Excluded.ID, d.Default.ID, d.Choice.ID}
}

// newSeedlessDefinitions declares parent_id and scope_id for node creation
// and excludes them from search. It replaces every other property name.
// Included and excluded text definitions declare search projections. Other
// text and select use type defaults for plain text and option labels.
func newSeedlessDefinitions(orgID uuid.UUID, keys seedlessKeys) seedlessDefinitions {
	definitions := seedlessDefinitions{
		Address:  seedlessDefinition(orgID, keys.Address, node.PropertyTypeText, seedlessProjection(false, 20)),
		Parent:   seedlessDefinition(orgID, seedlessParentProperty, node.PropertyTypeText, seedlessProjection(false, 50)),
		Scope:    seedlessDefinition(orgID, seedlessScopeProperty, node.PropertyTypeText, seedlessProjection(false, 60)),
		Included: seedlessDefinition(orgID, keys.Included, node.PropertyTypeText, seedlessProjection(true, 0)),
		Excluded: seedlessDefinition(orgID, keys.Excluded, node.PropertyTypeText, seedlessProjection(false, 1)),
		Default:  seedlessDefinition(orgID, keys.Default, node.PropertyTypeText, nil),
		Choice:   seedlessDefinition(orgID, keys.Choice, node.PropertyTypeSelect, nil),
	}
	for _, indexed := range []*node.PropertyDef{definitions.Address, definitions.Parent, definitions.Scope} {
		indexed.Indexed = true
	}
	definitions.Address.AppliesToFeatures = []string{node.FeatureHasAddress}
	definitions.Choice.Options = []node.EnumOption{
		{Key: keys.FirstOption, Label: firstOptionLabel, Color: "", SortRank: 0},
		{Key: keys.SecondOption, Label: secondOptionLabel, Color: "", SortRank: 1},
	}
	return definitions
}

func seedlessDefinition(orgID uuid.UUID, name string, kind node.PropertyType, projection *node.SearchProjection) *node.PropertyDef {
	return &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Type: kind, AppliesToFeatures: nil,
		Indexed: false, Options: nil, Required: false, DefaultValue: nil, DefaultReference: nil,
		ReferenceTargetTypeKey: "", Search: projection,
	}
}

func seedlessProjection(include bool, order int) *node.SearchProjection {
	return &node.SearchProjection{
		Include: include, Order: order,
		Rule: node.TextRule{Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil},
	}
}

type seedlessTypeSpec struct {
	Key          string
	Features     node.Features
	Reference    node.ReferenceConfig
	Definitions  []uuid.UUID
	CanContain   []string
	CanLiveUnder []string
}

func newSeedlessTypes(orgID uuid.UUID, keys seedlessKeys, definitions seedlessDefinitions) []*node.NodeType {
	address := node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: keys.Address}
	uuidOnly := node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly, Property: ""}
	addressOnly := []uuid.UUID{definitions.Address.ID}
	specs := []seedlessTypeSpec{
		{
			Key: keys.Root, Features: node.Features{node.FeatureHasAddress, node.FeatureIsContainer, node.FeatureIsScope, node.FeatureExcludeFromGenericTools},
			Reference: address, Definitions: addressOnly, CanContain: []string{keys.Entry}, CanLiveUnder: nil,
		},
		{
			Key: keys.Entry, Features: node.Features{node.FeatureHasAddress, node.FeatureIsContainer, node.FeatureIsEntryPoint, node.FeatureIsScope, node.FeatureExcludeFromGenericTools},
			Reference: address, Definitions: addressOnly, CanContain: []string{keys.Container}, CanLiveUnder: []string{keys.Root},
		},
		{
			Key: keys.Container, Features: node.Features{node.FeatureIsContainer}, Reference: uuidOnly,
			Definitions: definitions.content(), CanContain: []string{keys.Nested}, CanLiveUnder: []string{keys.Entry},
		},
		{
			Key: keys.Nested, Features: nil, Reference: uuidOnly,
			Definitions: definitions.content(), CanContain: nil, CanLiveUnder: []string{keys.Container},
		},
	}
	types := make([]*node.NodeType, 0, len(specs))
	for _, spec := range specs {
		types = append(types, &node.NodeType{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: spec.Key, Slug: spec.Key, Color: "", Icon: "",
			AllowedOps: node.AllOps, PropertyDefIDs: spec.Definitions, PluralSlug: spec.Key + "s",
			IsBuiltin: false, TypeKey: spec.Key, Features: spec.Features, CanContain: spec.CanContain,
			CanLiveUnder: spec.CanLiveUnder, Reference: spec.Reference, ReferenceTemplates: nil, DefaultChildren: nil,
		})
	}
	return types
}

// putSeedlessMetadata writes the required metadata with PropertyDefs.Set and
// NodeTypes.Set. Tack does not expose a public operation for creating
// property definitions or node types. ops qa datagen also uses these stores.
func putSeedlessMetadata(t *testing.T, env seedlessEnvironment, orgID uuid.UUID, keys seedlessKeys) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	definitions := newSeedlessDefinitions(orgID, keys)
	for _, definition := range definitions.all() {
		if err := env.Stores.PropertyDefs.Set(ctx, definition); err != nil {
			t.Fatalf("store property definition %s: %v", definition.Name, err)
		}
	}
	for _, kind := range newSeedlessTypes(orgID, keys, definitions) {
		if err := env.Stores.NodeTypes.Set(ctx, kind); err != nil {
			t.Fatalf("store node type %s: %v", kind.TypeKey, err)
		}
	}
}

func putSeedlessHierarchyNode(t *testing.T, env seedlessEnvironment, value *node.Node, parentID uuid.UUID, indexed []string) {
	t.Helper()
	relationships := make([]*node.Relationship, 0, 1)
	if parentID != uuid.Nil {
		value.Props[seedlessParentProperty] = json.RawMessage(strconv.Quote(parentID.String()))
		relationships = append(relationships, &node.Relationship{
			OrgID: value.OrgID, SourceID: value.ID, RelationType: node.RelChildOf, TargetID: parentID,
			CreatedBy: uuid.Nil, CreatedAt: value.CreatedAt, Props: nil,
		})
	}
	view := node.NodeView(*value)
	if err := env.Stores.Nodes.CreateAtomic(integration.WithTestOperation(t, t.Context()), value, &view, relationships, indexed, nil, nil); err != nil {
		t.Fatalf("create node %s of type %s: %v", value.ID, value.NodeType, err)
	}
}

func newSeedlessHierarchyNode(orgID, id uuid.UUID, typeKey, name, addressKey, address string) *node.Node {
	now := clock.Now().UTC()
	return &node.Node{
		ID: id, OrgID: orgID, NodeType: typeKey, Name: name,
		Props:     map[string]json.RawMessage{addressKey: json.RawMessage(strconv.Quote(address))},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
}
