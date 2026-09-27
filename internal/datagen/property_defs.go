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
	for _, definition := range qaPropertyDefs(orgID) {
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

// qaPropertyDefs declares every QA definition with an explicit search
// decision. The text and URL definitions include search. Every other QA
// definition excludes it.
func qaPropertyDefs(orgID uuid.UUID) []*node.PropertyDef {
	return []*node.PropertyDef{
		qaPropertyDef(orgID, "qa_text", node.PropertyTypeText, nil, qaSearch(true, 0)),
		qaPropertyDef(orgID, "qa_number", node.PropertyTypeNumber, nil, qaSearch(false, 1)),
		qaPropertyDef(orgID, "qa_date", node.PropertyTypeDate, nil, qaSearch(false, 2)),
		qaPropertyDef(orgID, "qa_select", node.PropertyTypeSelect, qaOptions(), qaSearch(false, 3)),
		qaPropertyDef(orgID, "qa_multi_select", node.PropertyTypeMultiSelect, qaOptions(), qaSearch(false, 4)),
		qaPropertyDef(orgID, "qa_url", node.PropertyTypeURL, nil, qaSearch(true, 5)),
		qaPropertyDef(orgID, "qa_checkbox", node.PropertyTypeCheckbox, nil, qaSearch(false, 6)),
		qaPropertyDef(orgID, "qa_timestamp", node.PropertyTypeTimestamp, nil, qaSearch(false, 7)),
		qaPropertyDef(orgID, "qa_uuid", node.PropertyTypeUUID, nil, qaSearch(false, 8)),
	}
}

func qaPropertyDef(
	orgID uuid.UUID,
	name string,
	propertyType node.PropertyType,
	options []node.EnumOption,
	search node.SearchProjection,
) *node.PropertyDef {
	return &node.PropertyDef{
		ID: node.SystemPropID(orgID, name), OrgID: orgID, Name: name,
		Type: propertyType, AppliesToFeatures: nil, Indexed: false,
		Options: options, Required: false, DefaultValue: nil,
		DefaultReference: nil, ReferenceTargetTypeKey: "",
		Search: &search,
	}
}

func qaSearch(include bool, order int) node.SearchProjection {
	return node.SearchProjection{
		Include: include,
		Order:   order,
		Rule:    node.TextRule{Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil},
	}
}

func qaOptions() []node.EnumOption {
	return []node.EnumOption{
		{Key: "planned", Label: "Planned", Color: "#64748B", SortRank: 0},
		{Key: "active", Label: "Active", Color: "#2563EB", SortRank: 1},
		{Key: "verified", Label: "Verified", Color: "#16A34A", SortRank: 2},
	}
}
