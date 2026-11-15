package mcp

import (
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// mutationSelection selects the requested identifiers.
func mutationSelection(
	arguments json.RawMessage,
	verb audit.Verb,
	referenceParameter string,
) *audit.ReadSelection {
	var values map[string]json.RawMessage
	if json.Unmarshal(arguments, &values) != nil {
		return nil
	}

	selection := &audit.ReadSelection{
		Reference: stringArgument(values, referenceParameter),
		Criteria:  nil,
	}

	fields := []string{"workspace_reference", "project_reference", "parent_id"}
	if relationshipMutation(verb) {
		fields = []string{"source_id", "target_id", "relation_type"}
	}

	for _, name := range fields {
		if stringArgument(values, name) != "" {
			if selection.Criteria == nil {
				selection.Criteria = make(map[string]json.RawMessage)
			}

			selection.Criteria[name] = values[name]
		}
	}

	if selection.Reference == "" && len(selection.Criteria) == 0 {
		return nil
	}

	return selection
}

// mutationTarget identifies the requested relationship endpoints.
func mutationTarget(
	verb audit.Verb,
	selection *audit.ReadSelection,
	target audit.Target,
) audit.Target {
	if !relationshipMutation(verb) || selection == nil {
		return target
	}

	sourceID, sourceErr := uuid.Parse(stringArgument(selection.Criteria, "source_id"))
	targetID, targetErr := uuid.Parse(stringArgument(selection.Criteria, "target_id"))
	relation := stringArgument(selection.Criteria, "relation_type")

	if sourceErr != nil || targetErr != nil || sourceID == uuid.Nil || targetID == uuid.Nil ||
		relation == "" {
		return target
	}

	target.Kind, target.URI, target.Name = audit.TargetKindRelationship, "", ""
	target.SourceID, target.TargetID, target.RelationType = sourceID, targetID, relation

	return target
}

func relationshipMutation(verb audit.Verb) bool {
	return verb == audit.VerbRelationshipAdd || verb == audit.VerbRelationshipRemove
}
