package integration_test

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type seedlessTypeSpec struct {
	Key          string
	Features     node.Features
	Reference    node.ReferenceConfig
	Definitions  []uuid.UUID
	CanContain   []string
	CanLiveUnder []string
}

func newSeedlessTypes(
	orgID uuid.UUID,
	keys seedlessKeys,
	definitions seedlessDefinitions,
) []*node.NodeType {
	address := node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: keys.Address}
	uuidOnly := node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly, Property: ""}
	addressOnly := []uuid.UUID{definitions.Address.ID}
	specs := []seedlessTypeSpec{
		{
			Key: keys.Root,
			Features: node.Features{
				node.FeatureHasAddress,
				node.FeatureIsContainer,
				node.FeatureIsScope,
				node.FeatureExcludeFromGenericTools,
			},
			Reference:    address,
			Definitions:  addressOnly,
			CanContain:   []string{keys.Entry},
			CanLiveUnder: nil,
		},
		{
			Key: keys.Entry,
			Features: node.Features{
				node.FeatureHasAddress,
				node.FeatureIsContainer,
				node.FeatureIsEntryPoint,
				node.FeatureIsScope,
				node.FeatureExcludeFromGenericTools,
			},
			Reference:    address,
			Definitions:  addressOnly,
			CanContain:   []string{keys.Container},
			CanLiveUnder: []string{keys.Root},
		},
		{
			Key:          keys.Container,
			Features:     node.Features{node.FeatureIsContainer},
			Reference:    uuidOnly,
			Definitions:  definitions.content(),
			CanContain:   []string{keys.Nested},
			CanLiveUnder: []string{keys.Entry},
		},
		{
			Key:          keys.Nested,
			Features:     nil,
			Reference:    uuidOnly,
			Definitions:  definitions.content(),
			CanContain:   nil,
			CanLiveUnder: []string{keys.Container},
		},
	}

	types := make([]*node.NodeType, 0, len(specs))
	for _, spec := range specs {
		types = append(
			types,
			&node.NodeType{
				ID:                 uuid.Must(uuid.NewV7()),
				OrgID:              orgID,
				Name:               spec.Key,
				Slug:               spec.Key,
				Color:              "",
				Icon:               "",
				AllowedOps:         node.AllOps,
				PropertyDefIDs:     spec.Definitions,
				PluralSlug:         spec.Key + "s",
				IsBuiltin:          false,
				TypeKey:            spec.Key,
				Features:           spec.Features,
				CanContain:         spec.CanContain,
				CanLiveUnder:       spec.CanLiveUnder,
				Reference:          spec.Reference,
				ReferenceTemplates: nil,
				DefaultChildren:    nil,
			},
		)
	}

	return types
}
