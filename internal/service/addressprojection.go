package service

import (
	"fmt"

	"goodkind.io/tack/internal/domain/node"
)

// seedSearchDecisionError is the refusal of a seeded definition without a
// search decision. SeedOrg logs it once.
type seedSearchDecisionError struct {
	name string
}

func (e seedSearchDecisionError) Error() string {
	return fmt.Sprintf(
		"seeded property definition %q has no search decision: %v",
		e.name,
		node.ErrInvalidSearchProjection,
	)
}

// Unwrap returns the search projection validation error.
//
// Callers can match node.ErrInvalidSearchProjection with [errors.Is].
//
//	errors.Is(err, node.ErrInvalidSearchProjection)
func (e seedSearchDecisionError) Unwrap() error { return node.ErrInvalidSearchProjection }

// applyDefaultSearchProjections sets the search decision of each definition.
//
// The seed refuses a definition when its name does not have a search order.
//
//	err := applyDefaultSearchProjections(definitions)
func applyDefaultSearchProjections(definitions []*node.PropertyDef) error {
	orders := map[string]int{
		propertyParentID:    searchParentOrder,
		"priority":          0,
		"due_date":          1,
		"start_date":        searchStartDateOrder,
		propertyStateID:     searchStateOrder,
		"is_draft":          searchDraftOrder,
		propertyDescription: searchDescriptionOrder,
		propertySlug:        searchSlugOrder,
		propertyIdentifier:  searchIdentifierOrder,
		propertySequence:    searchSequenceOrder,
		propertyScopeID:     searchScopeOrder,
		propertyGroup:       searchGroupOrder,
		propertyColor:       searchColorOrder,
		propertySortOrder:   searchSortOrder,
	}
	included := map[string]bool{
		propertyParentID:    false,
		propertyDescription: true,
		propertySlug:        true,
		propertyIdentifier:  true,
		propertySequence:    true,
		propertyGroup:       true,
		propertyScopeID:     false,
		propertyStateID:     false,
	}

	for _, definition := range definitions {
		order, ok := orders[definition.Name]
		if !ok {
			return seedSearchDecisionError{name: definition.Name}
		}

		definition.Search = &node.SearchProjection{
			Include: included[definition.Name],
			Order:   order,
			Rule: node.TextRule{
				Mode:   node.TextRuleScalar,
				Fields: nil,
				Items:  nil,
				Labels: nil,
			},
		}
	}

	return nil
}

// scopedSequenceTemplate joins the scope reference, a hyphen, and a
// generated
// sequence. Types using this template share numbering within each scope
// because
// the template omits the node type.
// The engine does not supply a default template or constrain types without
// one.
// SeedOrg overwrites built-in templates on reseed. Org-defined types
// preserve
// custom templates because the seed does not update those types.
func scopedSequenceTemplate() []node.ReferenceTemplate {
	return []node.ReferenceTemplate{
		{
			Name:      "reference",
			IsPrimary: true,
			Generated: propertySequence,
			Parts: []node.ReferencePart{
				{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
				{Kind: node.ReferencePartLiteral, Value: "-"},
				{Kind: node.ReferencePartProperty, Value: propertySequence},
			},
		},
	}
}
