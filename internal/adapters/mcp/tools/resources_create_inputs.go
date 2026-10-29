package tools

import (
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

type createInputTemplateRow struct {
	Tool         string
	Required     []createInputParam
	Alternatives []createInputParam
}

type createInputParam struct {
	Name    string
	Example string
}

// createInputRows builds create tool guidance for node types that allow OpCreate
// and do not have FeatureExcludeFromGenericTools. It separates required inputs
// from alternative parent inputs.
func createInputRows(resolver *Resolver, nodeTypes []*node.NodeType) []createInputTemplateRow {
	var rows []createInputTemplateRow
	for _, nodeType := range nodeTypes {
		if nodeType.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}
		if _, ok := opSet(nodeType.AllowedOps)[node.OpCreate]; !ok {
			continue
		}
		route := resolver.ScopeRouteForType(nodeType)
		required := []createInputParam{
			{Name: resolver.EntryPointParamName(), Example: ""},
			{Name: "name", Example: ""},
		}
		for _, level := range requiredScopeLevels(route) {
			required = append(required, scopeInputParam(resolver, level))
		}
		alternatives := make([]createInputParam, 0, len(route.Leaves))
		for _, leaf := range route.Leaves {
			alternatives = append(alternatives, scopeInputParam(resolver, leaf.Level))
		}
		rows = append(rows, createInputTemplateRow{
			Tool:         "tack_create_" + strings.ToLower(nodeType.Slug),
			Required:     required,
			Alternatives: alternatives,
		})
	}
	return rows
}

// requiredScopeLevels returns levels for route.requiredParams in chain order.
// It uses the first leaf's chain when the route has leaves.
func requiredScopeLevels(route scopeRoute) []ScopeLevel {
	requiredNames := route.requiredParams()
	levels := make([]ScopeLevel, 0, len(requiredNames))
	candidates := route.Chain
	if len(route.Leaves) > 0 {
		candidates = route.Leaves[0].Chain
	}
	for _, level := range candidates {
		for _, name := range requiredNames {
			if level.ParamName == name {
				levels = append(levels, level)
			}
		}
	}
	return levels
}

func scopeInputParam(resolver *Resolver, level ScopeLevel) createInputParam {
	example := referenceExample(scopeReference(scopeTypeName(level, resolver)))
	return createInputParam{Name: level.ParamName, Example: example}
}
