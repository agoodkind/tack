package node

import (
	"encoding/json"
	"sort"

	"github.com/google/uuid"
)

// Schema contains one organization's node types by type key and property
// definitions.
type Schema struct {
	OrgID       uuid.UUID
	Types       map[string]*NodeType
	Definitions []*PropertyDef
}

// PropertyAppliesTo reports whether definition applies to nodeType. A
// definition without features applies to every node type. Otherwise,
// nodeType must declare one of the features that definition lists.
func PropertyAppliesTo(definition *PropertyDef, nodeType *NodeType) bool {
	if len(definition.AppliesToFeatures) == 0 {
		return true
	}
	return nodeType.Features.HasAny(definition.AppliesToFeatures...)
}

// Interpret checks names, applicability, JSON shapes, formats, and option
// keys in props against the definitions for typeKey. It does not check
// required presence or reference targets.
func (s Schema) Interpret(typeKey string, props map[string]json.RawMessage) PropertyResult {
	result := PropertyResult{
		NodeType: typeKey, Valid: make(map[string]json.RawMessage, len(props)),
		Invalid: nil, Failures: nil,
	}
	nodeType := s.Types[typeKey]
	if nodeType == nil {
		result.Failures = append(result.Failures, ReadFailure{Subject: "node type " + typeKey, Reason: "the schema does not contain it"})
		return result
	}
	definitions, failures := s.definitionsByName()
	result.Failures = append(result.Failures, failures...)
	for _, name := range sortedPropertyNames(props) {
		raw := props[name]
		definition := definitions[name]
		if definition == nil {
			result.Invalid = append(result.Invalid, PropertyIssue{Property: name, Code: PropertyIssueUndeclared, Reason: "no property definition declares it"})
			continue
		}
		if !PropertyAppliesTo(definition, nodeType) {
			result.Invalid = append(result.Invalid, PropertyIssue{Property: name, Code: PropertyIssueNotApplicable, Reason: "its definition does not apply to node type " + typeKey})
			continue
		}
		if issue := propertyValueIssue(definition, raw); issue != nil {
			result.Invalid = append(result.Invalid, *issue)
			continue
		}
		result.Valid[name] = raw
	}
	return result
}

func (s Schema) definitionsByName() (map[string]*PropertyDef, []ReadFailure) {
	definitions := make(map[string]*PropertyDef, len(s.Definitions))
	var failures []ReadFailure
	for _, definition := range s.Definitions {
		if definition == nil {
			continue
		}
		if _, exists := definitions[definition.Name]; exists {
			failures = append(failures, ReadFailure{Subject: "property definition " + definition.Name, Reason: "the schema contains the name more than once"})
			continue
		}
		definitions[definition.Name] = definition
	}
	return definitions, failures
}

func sortedPropertyNames(props map[string]json.RawMessage) []string {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
