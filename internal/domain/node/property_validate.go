package node

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	// PropertyIssueRequired marks an applicable required property without a
	// value.
	PropertyIssueRequired PropertyIssueCode = "missing_required"
	// PropertyIssueReference marks a reference property with a target that
	// does not exist in the organization or has a different node type.
	PropertyIssueReference PropertyIssueCode = "invalid_reference"
	// PropertyIssueParent marks a missing or invalid child_of relationship.
	PropertyIssueParent PropertyIssueCode = "invalid_parent"
)

// ResultTarget records whether a node has a resolution record, along with
// its organization ID and node type.
type ResultTarget struct {
	Found    bool
	OrgID    uuid.UUID
	NodeType string
}

// ResultGraph provides graph data for ValidateResult. OrgID identifies the
// result's organization. ChildOf lists the target IDs of its child_of edges.
// Targets maps IDs from child_of edges and reference properties to ResultTarget
// values.
type ResultGraph struct {
	OrgID   uuid.UUID
	ChildOf []uuid.UUID
	Targets map[uuid.UUID]ResultTarget
}

// ValidateResult runs Interpret on the complete properties of a node of
// typeKey, then checks required properties, references, and parent rules.
// A node requires a parent when its type lists CanLiveUnder types. When a
// parent exists, exactly one child_of edge must target a node in the
// same organization, Types must contain the parent's node type, and
// [LivesUnder] must accept the child and parent types.
func (s Schema) ValidateResult(
	typeKey string,
	props map[string]json.RawMessage,
	graph ResultGraph,
) PropertyResult {
	result := s.Interpret(typeKey, props)
	nodeType := s.Types[typeKey]
	if nodeType == nil || len(result.Failures) > 0 {
		return result
	}

	rejected := make(map[string]bool, len(result.Invalid))
	for _, issue := range result.Invalid {
		rejected[issue.Property] = true
	}

	for _, definition := range s.sortedDefinitions() {
		if !PropertyAppliesTo(definition, nodeType) || rejected[definition.Name] {
			continue
		}

		raw, present := result.Valid[definition.Name]
		if definition.Required && !present {
			result.Invalid = append(
				result.Invalid,
				PropertyIssue{
					Property: definition.Name,
					Code:     PropertyIssueRequired,
					Reason:   "the property is required",
				},
			)
			continue
		}

		if !present || definition.Type != PropertyTypeUUID ||
			definition.ReferenceTargetTypeKey == "" {
			continue
		}

		if reason := referenceReason(definition, raw, graph); reason != "" {
			delete(result.Valid, definition.Name)
			result.Invalid = append(
				result.Invalid,
				PropertyIssue{
					Property: definition.Name,
					Code:     PropertyIssueReference,
					Reason:   reason,
				},
			)
		}
	}

	reason, failure := s.parentReason(nodeType, graph)
	if failure != nil {
		result.Failures = append(result.Failures, *failure)
		return result
	}

	if reason != "" {
		result.Invalid = append(
			result.Invalid,
			PropertyIssue{Property: RelChildOf, Code: PropertyIssueParent, Reason: reason},
		)
	}

	return result
}

// sortedDefinitions returns the definitions in name order without nil
// entries.
func (s Schema) sortedDefinitions() []*PropertyDef {
	definitions := make([]*PropertyDef, 0, len(s.Definitions))
	for _, definition := range s.Definitions {
		if definition != nil {
			definitions = append(definitions, definition)
		}
	}

	slices.SortFunc(
		definitions,
		func(left, right *PropertyDef) int { return strings.Compare(left.Name, right.Name) },
	)

	return definitions
}

// referenceReason returns the reason that the reference in raw does not
// satisfy definition, or "" for a valid reference.
func referenceReason(definition *PropertyDef, raw json.RawMessage, graph ResultGraph) string {
	targetID := resultReferenceID(raw)
	target := graph.Targets[targetID]
	if !target.Found || target.OrgID != graph.OrgID {
		return fmt.Sprintf("the node %s does not exist in the organization", targetID)
	}

	if target.NodeType != definition.ReferenceTargetTypeKey {
		return fmt.Sprintf(
			"the node %s has type %q, and the definition requires type %q",
			targetID,
			target.NodeType,
			definition.ReferenceTargetTypeKey,
		)
	}

	return ""
}
