package integration_test

import (
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
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
		Root:         opaqueSeedlessKey("t"),
		Entry:        opaqueSeedlessKey("t"),
		Container:    opaqueSeedlessKey("t"),
		Nested:       opaqueSeedlessKey("t"),
		Address:      opaqueSeedlessKey("p"),
		Included:     opaqueSeedlessKey("p"),
		Excluded:     opaqueSeedlessKey("p"),
		Default:      opaqueSeedlessKey("p"),
		Choice:       opaqueSeedlessKey("p"),
		FirstOption:  opaqueSeedlessKey("o"),
		SecondOption: opaqueSeedlessKey("o"),
	}
}

func (k seedlessKeys) all() []string {
	return []string{
		k.Root,
		k.Entry,
		k.Container,
		k.Nested,
		k.Address,
		k.Included,
		k.Excluded,
		k.Default,
		k.Choice,
		k.FirstOption,
		k.SecondOption,
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
	return []*node.PropertyDef{
		d.Address,
		d.Parent,
		d.Scope,
		d.Included,
		d.Excluded,
		d.Default,
		d.Choice,
	}
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
		Address: seedlessDefinition(
			orgID,
			keys.Address,
			node.PropertyTypeText,
			seedlessProjection(false, 20),
		),
		Parent: seedlessDefinition(
			orgID,
			seedlessParentProperty,
			node.PropertyTypeText,
			seedlessProjection(false, 50),
		),
		Scope: seedlessDefinition(
			orgID,
			seedlessScopeProperty,
			node.PropertyTypeText,
			seedlessProjection(false, 60),
		),
		Included: seedlessDefinition(
			orgID,
			keys.Included,
			node.PropertyTypeText,
			seedlessProjection(true, 0),
		),
		Excluded: seedlessDefinition(
			orgID,
			keys.Excluded,
			node.PropertyTypeText,
			seedlessProjection(false, 1),
		),
		Default: seedlessDefinition(orgID, keys.Default, node.PropertyTypeText, nil),
		Choice:  seedlessDefinition(orgID, keys.Choice, node.PropertyTypeSelect, nil),
	}
	for _, indexed := range []*node.PropertyDef{
		definitions.Address,
		definitions.Parent,
		definitions.Scope,
	} {
		indexed.Indexed = true
	}

	definitions.Address.AppliesToFeatures = []string{node.FeatureHasAddress}
	definitions.Choice.Options = []node.EnumOption{
		{Key: keys.FirstOption, Label: firstOptionLabel, Color: "", SortRank: 0},
		{Key: keys.SecondOption, Label: secondOptionLabel, Color: "", SortRank: 1},
	}

	return definitions
}

func seedlessDefinition(
	orgID uuid.UUID,
	name string,
	kind node.PropertyType,
	projection *node.SearchProjection,
) *node.PropertyDef {
	return &node.PropertyDef{
		ID:                     uuid.Must(uuid.NewV7()),
		OrgID:                  orgID,
		Name:                   name,
		Type:                   kind,
		AppliesToFeatures:      nil,
		Indexed:                false,
		Options:                nil,
		Required:               false,
		DefaultValue:           nil,
		DefaultReference:       nil,
		ReferenceTargetTypeKey: "",
		Search:                 projection,
	}
}

func seedlessProjection(include bool, order int) *node.SearchProjection {
	return &node.SearchProjection{
		Include: include,
		Order:   order,
		Rule:    node.TextRule{Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil},
	}
}
