package node

import (
	"fmt"
	"strings"
)

// defaultSearchOrder assigns the default projection order. Default
// projections sort by definition ID when their orders match.
const defaultSearchOrder = 1 << 20

// EffectiveSearchProjection returns the declared projection when the
// definition supplies one. Otherwise, it selects a default projection.
//
// The default excludes node references with a nonempty
// ReferenceTargetTypeKey. The remaining property types use these rules:
//
//   - Text, URL, number, date, and timestamp render scalar values.
//   - Select renders the option label.
//   - Multi-select renders each item's label.
//   - Checkbox and UUID do not render.
//   - An unknown type returns an error when it lacks a declaration.
func EffectiveSearchProjection(definition *PropertyDef) (SearchProjection, error) {
	if definition.Search != nil {
		return *definition.Search, nil
	}

	scalar := TextRule{Mode: TextRuleScalar, Fields: nil, Items: nil, Labels: nil}
	labels := TextRule{
		Mode:   TextRuleLabels,
		Fields: nil,
		Items:  nil,
		Labels: optionLabels(definition.Options),
	}
	if definition.ReferenceTargetTypeKey != "" {
		return SearchProjection{Include: false, Order: defaultSearchOrder, Rule: scalar}, nil
	}

	switch definition.Type {
	case PropertyTypeText,
		PropertyTypeURL,
		PropertyTypeNumber,
		PropertyTypeDate,
		PropertyTypeTimestamp:
		return SearchProjection{Include: true, Order: defaultSearchOrder, Rule: scalar}, nil
	case PropertyTypeSelect:
		return SearchProjection{Include: true, Order: defaultSearchOrder, Rule: labels}, nil
	case PropertyTypeMultiSelect:
		items := TextRule{Mode: TextRuleItems, Fields: nil, Items: &labels, Labels: nil}
		return SearchProjection{Include: true, Order: defaultSearchOrder, Rule: items}, nil
	case PropertyTypeCheckbox, PropertyTypeUUID:
		return SearchProjection{Include: false, Order: defaultSearchOrder, Rule: scalar}, nil
	default:
		return SearchProjection{}, definitionError{
			message: fmt.Sprintf(
				"property definition %s uses unknown type %q without a search declaration",
				definition.ID,
				definition.Type,
			),
		}
	}
}

// optionLabels maps each option key to its label. An option without a label
// uses its key.
func optionLabels(options []EnumOption) map[string]string {
	labels := make(map[string]string, len(options))
	for _, option := range options {
		label := option.Label
		if strings.TrimSpace(label) == "" {
			label = option.Key
		}

		labels[option.Key] = label
	}

	return labels
}
