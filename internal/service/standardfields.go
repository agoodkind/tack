package service

import (
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func defaultPropertyDefs(orgID uuid.UUID) ([]*node.PropertyDef, error) {
	definitions := make([]*node.PropertyDef, 0, defaultPropertyDefinitionCount)
	definitions = append(
		definitions,
		[]*node.PropertyDef{
			defaultPriorityDefinition(orgID),
			{
				ID:                     node.SystemPropID(orgID, "due_date"),
				OrgID:                  orgID,
				Name:                   "due_date",
				Type:                   node.PropertyTypeTimestamp,
				AppliesToFeatures:      []string{node.FeatureHasDueDates},
				Indexed:                true,
				Options:                nil,
				Required:               false,
				DefaultValue:           nil,
				DefaultReference:       nil,
				ReferenceTargetTypeKey: "",
				Search:                 nil,
			},
			{
				ID:                     node.SystemPropID(orgID, "start_date"),
				OrgID:                  orgID,
				Name:                   "start_date",
				Type:                   node.PropertyTypeTimestamp,
				AppliesToFeatures:      []string{node.FeatureHasDueDates},
				Indexed:                true,
				Options:                nil,
				Required:               false,
				DefaultValue:           nil,
				DefaultReference:       nil,
				ReferenceTargetTypeKey: "",
				Search:                 nil,
			},
			{
				ID:                     node.SystemPropID(orgID, propertyStateID),
				OrgID:                  orgID,
				Name:                   propertyStateID,
				Type:                   node.PropertyTypeUUID,
				AppliesToFeatures:      []string{node.FeatureHasWorkflowStates},
				Indexed:                true,
				Required:               true,
				ReferenceTargetTypeKey: nodeTypeState,
				DefaultReference: &node.PropertyDefaultReference{
					Scope:     node.DefaultReferenceScopeCreateScope,
					Reference: "Todo",
				},
				Options:      nil,
				DefaultValue: nil,
				Search:       nil,
			},
			{
				ID:                     node.SystemPropID(orgID, "is_draft"),
				OrgID:                  orgID,
				Name:                   "is_draft",
				Type:                   node.PropertyTypeCheckbox,
				AppliesToFeatures:      []string{node.FeatureHasWorkflowStates},
				Indexed:                false,
				DefaultValue:           json.RawMessage("false"),
				Options:                nil,
				Required:               false,
				DefaultReference:       nil,
				ReferenceTargetTypeKey: "",
				Search:                 nil,
			},
		}...,
	)
	definitions = append(definitions, additionalPropertyDefs(orgID)...)
	if err := applyDefaultSearchProjections(definitions); err != nil {
		return nil, err
	}

	return definitions, nil
}

func defaultPriorityDefinition(orgID uuid.UUID) *node.PropertyDef {
	return &node.PropertyDef{
		ID:                node.SystemPropID(orgID, "priority"),
		OrgID:             orgID,
		Name:              "priority",
		Type:              node.PropertyTypeSelect,
		AppliesToFeatures: []string{node.FeatureHasWorkflowStates},
		Indexed:           true,
		Options: []node.EnumOption{
			{Key: "urgent", Label: "Urgent", Color: "#EF4444", SortRank: 0},
			{Key: "high", Label: "High", Color: "#F97316", SortRank: 1},
			{Key: "medium", Label: "Medium", Color: "#EAB308", SortRank: priorityMediumRank},
			{Key: "low", Label: "Low", Color: "#22C55E", SortRank: priorityLowRank},
			{Key: "none", Label: "No Priority", Color: "#9B9B9B", SortRank: priorityNoneRank},
		},
		DefaultValue:           mustRawString("none"),
		Required:               false,
		DefaultReference:       nil,
		ReferenceTargetTypeKey: "",
		Search:                 nil,
	}
}
