package service

import "goodkind.io/tack/internal/domain/node"

const (
	defaultIdentifierProperty = "identifier"
	defaultSlugProperty       = "slug"
	defaultSequenceProperty   = "sequence"
	defaultGroupProperty      = "group"
	defaultColorProperty      = "color"
	defaultSortOrderProperty  = "sort_order"
	defaultStartedStateRank   = 2
	defaultCompletedStateRank = 3
	defaultCancelledStateRank = 4
)

type builtinTypeSpec struct {
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
