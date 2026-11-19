package service

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type builtinNodeSpec struct {
	slug            string
	pluralSlug      string
	name            string
	typeKey         string
	features        node.Features
	reference       node.ReferenceConfig
	canContain      []string
	canLiveUnder    []string
	defaultChildren []node.DefaultChild
}

// defaultNodeTypes returns the built-in NodeType records for a given org.
// IDs are deterministic UUID v5 values derived from (orgID, NodeType.Slug)
// so
// repeated seeding writes the same record.
func defaultNodeTypes(orgID uuid.UUID) []*node.NodeType {
	specs := append(defaultContainerSpecs(), defaultWorkSpecs()...)
	specs = append(specs, defaultSupportSpecs()...)

	types := make([]*node.NodeType, 0, len(specs))
	for _, specification := range specs {
		typeID := uuid.NewSHA1(builtinTypeNamespace, []byte(orgID.String()+":"+specification.slug))
		var referenceTemplates []node.ReferenceTemplate
		if specification.features.Has(node.FeatureHasSequenceID) {
			referenceTemplates = scopedSequenceTemplate()
		}

		types = append(
			types,
			&node.NodeType{
				ID:                 typeID,
				OrgID:              orgID,
				Name:               specification.name,
				Slug:               specification.slug,
				PluralSlug:         specification.pluralSlug,
				IsBuiltin:          true,
				TypeKey:            specification.typeKey,
				AllowedOps:         node.AllOps,
				Features:           specification.features,
				CanContain:         specification.canContain,
				CanLiveUnder:       specification.canLiveUnder,
				Reference:          specification.reference,
				ReferenceTemplates: referenceTemplates,
				DefaultChildren:    specification.defaultChildren,
				Color:              "",
				Icon:               "",
				PropertyDefIDs:     nil,
			},
		)
	}

	return types
}
