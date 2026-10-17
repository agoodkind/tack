package node

import (
	"encoding/json"
	"fmt"
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
		Invalid: nil, References: nil, Failures: nil,
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
		if reference, ok := propertyReference(definition, raw); ok {
			result.References = append(result.References, reference)
		}
	}
	return result
}

// ValidateResult checks the complete resulting properties of one node.
// The typeKey parameter is the node type key. ValidateResult applies
// [Schema.Interpret], requires every applicable required property, and
// checks each reference value against references. A reference value
// without an entry in references is a read failure.
func (s Schema) ValidateResult(typeKey string, props map[string]json.RawMessage, references map[uuid.UUID]ReferenceRecord) PropertyResult {
	result := s.Interpret(typeKey, props)
	nodeType := s.Types[typeKey]
	if nodeType == nil {
		return result
	}
	for _, definition := range s.Definitions {
		if definition == nil || !definition.Required || !PropertyAppliesTo(definition, nodeType) {
			continue
		}
		if _, present := props[definition.Name]; !present {
			result.Invalid = append(result.Invalid, PropertyIssue{Property: definition.Name, Code: PropertyIssueRequired, Reason: "the property is required"})
		}
	}
	checked := make([]PropertyReference, 0, len(result.References))
	for _, reference := range result.References {
		record, read := references[reference.TargetID]
		if !read {
			result.Failures = append(result.Failures, ReadFailure{Subject: "properties." + reference.Property, Reason: "reference record " + reference.TargetID.String() + " was not read"})
			continue
		}
		if issue := s.referenceIssue(reference, record); issue != nil {
			delete(result.Valid, reference.Property)
			result.Invalid = append(result.Invalid, *issue)
			continue
		}
		checked = append(checked, reference)
	}
	result.References = checked
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

func (s Schema) referenceIssue(reference PropertyReference, record ReferenceRecord) *PropertyIssue {
	var reason string
	switch {
	case s.Types[reference.TargetType] == nil:
		reason = fmt.Sprintf("target node type %q is not declared", reference.TargetType)
	case !record.Exists:
		reason = fmt.Sprintf("node %s does not exist", reference.TargetID)
	case record.OrgID != s.OrgID:
		reason = fmt.Sprintf("node %s belongs to another organization", reference.TargetID)
	case record.NodeType != reference.TargetType:
		reason = fmt.Sprintf("node %s has type %q, and the property requires type %q", reference.TargetID, record.NodeType, reference.TargetType)
	default:
		return nil
	}
	return &PropertyIssue{Property: reference.Property, Code: PropertyIssueReference, Reason: reason}
}

func sortedPropertyNames(props map[string]json.RawMessage) []string {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
