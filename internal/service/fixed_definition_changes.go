package service

import (
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const (
	fixedOutcomeCorrected  = "corrected"
	fixedOutcomeConflicted = "conflicted"
)

// FixedDefinitionChange records the org ID, property definition ID, name,
// and outcome for one corrected or conflicted property definition. It
// includes before and after values only for fields that need correction.
type FixedDefinitionChange struct {
	OrgID             uuid.UUID          `json:"org_id"`
	PropertyDefID     uuid.UUID          `json:"property_def_id"`
	Name              string             `json:"name"`
	Outcome           string             `json:"outcome"`
	Search            *FixedSearchChange `json:"search,omitempty"`
	AppliesToFeatures *FixedListChange   `json:"applies_to_features,omitempty"`
}

// FixedSearchChange records the stored and corrected Search projections
// as *node.SearchProjection values. A nil stored value means the definition
// did not declare a search projection.
type FixedSearchChange struct {
	Before *node.SearchProjection `json:"before"`
	After  *node.SearchProjection `json:"after"`
}

// FixedListChange records the stored and corrected values of one string
// list field.
type FixedListChange struct {
	Before []string `json:"before"`
	After  []string `json:"after"`
}

// FixedNodeTypeChange records the org ID, node type ID, type key, and
// outcome for one corrected or conflicted node type. It always includes
// the stored and corrected CanLiveUnder lists.
type FixedNodeTypeChange struct {
	OrgID        uuid.UUID       `json:"org_id"`
	NodeTypeID   uuid.UUID       `json:"node_type_id"`
	TypeKey      string          `json:"type_key"`
	Outcome      string          `json:"outcome"`
	CanLiveUnder FixedListChange `json:"can_live_under"`
}

// definitionChange builds a FixedDefinitionChange from the outcome and
// the stored and corrected definitions. correctedDefinition copies the
// stored definition and preserves its Search pointer unless a correction
// assigns a new projection. definitionChange compares the Search pointers
// to detect a Search change and uses [slices.Equal] to detect an
// AppliesToFeatures change.
func definitionChange(outcome string, stored, corrected *node.PropertyDef) FixedDefinitionChange {
	change := FixedDefinitionChange{
		OrgID: stored.OrgID, PropertyDefID: stored.ID, Name: stored.Name, Outcome: outcome, Search: nil, AppliesToFeatures: nil,
	}
	if stored.Search != corrected.Search {
		change.Search = &FixedSearchChange{Before: stored.Search, After: corrected.Search}
	}
	if !slices.Equal(stored.AppliesToFeatures, corrected.AppliesToFeatures) {
		change.AppliesToFeatures = &FixedListChange{Before: stored.AppliesToFeatures, After: corrected.AppliesToFeatures}
	}
	return change
}

func nodeTypeChange(outcome string, stored, corrected *node.NodeType) FixedNodeTypeChange {
	return FixedNodeTypeChange{
		OrgID: stored.OrgID, NodeTypeID: stored.ID, TypeKey: stored.TypeKey, Outcome: outcome,
		CanLiveUnder: FixedListChange{Before: stored.CanLiveUnder, After: corrected.CanLiveUnder},
	}
}
