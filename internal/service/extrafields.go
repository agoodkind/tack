package service

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func additionalPropertyDefs(orgID uuid.UUID) []*node.PropertyDef {
	definitions := additionalIdentityDefs(orgID)

	return append(definitions, additionalDisplayDefs(orgID)...)
}

func additionalIdentityDefs(orgID uuid.UUID) []*node.PropertyDef {
	return []*node.PropertyDef{
		{
			ID:                     node.SystemPropID(orgID, propertyDescription),
			OrgID:                  orgID,
			Name:                   propertyDescription,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      nil,
			Indexed:                false,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertySlug),
			OrgID:                  orgID,
			Name:                   propertySlug,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      []string{node.FeatureHasAddress},
			Indexed:                true,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertyIdentifier),
			OrgID:                  orgID,
			Name:                   propertyIdentifier,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      []string{node.FeatureIsScope},
			Indexed:                true,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertySequence),
			OrgID:                  orgID,
			Name:                   propertySequence,
			Type:                   node.PropertyTypeNumber,
			AppliesToFeatures:      []string{node.FeatureHasSequenceID},
			Indexed:                true,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertyScopeID),
			OrgID:                  orgID,
			Name:                   propertyScopeID,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      nil,
			Indexed:                true,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
	}
}

func additionalDisplayDefs(orgID uuid.UUID) []*node.PropertyDef {
	return []*node.PropertyDef{
		{
			ID:                     node.SystemPropID(orgID, propertyGroup),
			OrgID:                  orgID,
			Name:                   propertyGroup,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      nil,
			Indexed:                false,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertyColor),
			OrgID:                  orgID,
			Name:                   propertyColor,
			Type:                   node.PropertyTypeText,
			AppliesToFeatures:      nil,
			Indexed:                false,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
		{
			ID:                     node.SystemPropID(orgID, propertySortOrder),
			OrgID:                  orgID,
			Name:                   propertySortOrder,
			Type:                   node.PropertyTypeNumber,
			AppliesToFeatures:      nil,
			Indexed:                false,
			Options:                nil,
			Required:               false,
			DefaultValue:           nil,
			DefaultReference:       nil,
			ReferenceTargetTypeKey: "",
			Search:                 nil,
		},
	}
}
