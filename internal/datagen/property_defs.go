package datagen

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type propertyDefWriter interface {
	Set(ctx context.Context, def *node.PropertyDef) error
}

func seedQAPropertyDefs(
	ctx context.Context,
	writer propertyDefWriter,
	orgID uuid.UUID,
) error {
	definitions := []*node.PropertyDef{
		qaPropertyDef(orgID, "qa_text", node.PropertyTypeText, nil),
		qaPropertyDef(orgID, "qa_number", node.PropertyTypeNumber, nil),
		qaPropertyDef(orgID, "qa_date", node.PropertyTypeDate, nil),
		qaPropertyDef(orgID, "qa_select", node.PropertyTypeSelect, qaOptions()),
		qaPropertyDef(orgID, "qa_multi_select", node.PropertyTypeMultiSelect, qaOptions()),
		qaPropertyDef(orgID, "qa_url", node.PropertyTypeURL, nil),
		qaPropertyDef(orgID, "qa_checkbox", node.PropertyTypeCheckbox, nil),
		qaPropertyDef(orgID, "qa_timestamp", node.PropertyTypeTimestamp, nil),
		qaPropertyDef(orgID, "qa_uuid", node.PropertyTypeUUID, nil),
	}
	for _, definition := range definitions {
		if err := writer.Set(ctx, definition); err != nil {
			return loggedError(
				ctx,
				"qa datagen: seed property definition "+definition.Name,
				err,
			)
		}
	}
	return nil
}

func qaPropertyDef(
	orgID uuid.UUID,
	name string,
	propertyType node.PropertyType,
	options []node.EnumOption,
) *node.PropertyDef {
	return &node.PropertyDef{
		ID: node.SystemPropID(orgID, name), OrgID: orgID, Name: name,
		Type: propertyType, AppliesToFeatures: nil, Indexed: false,
		Options: options, Required: false, DefaultValue: nil,
		DefaultReference: nil, ReferenceTargetTypeKey: "",
	}
}

func qaOptions() []node.EnumOption {
	return []node.EnumOption{
		{Key: "planned", Label: "Planned", Color: "#64748B", SortRank: 0},
		{Key: "active", Label: "Active", Color: "#2563EB", SortRank: 1},
		{Key: "verified", Label: "Verified", Color: "#16A34A", SortRank: 2},
	}
}
