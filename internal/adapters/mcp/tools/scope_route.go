package tools

import (
	"slices"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

// scopeLeaf pairs an allowed direct parent with its ancestor scope levels.
type scopeLeaf struct {
	Level ScopeLevel
	Chain []ScopeLevel
}

// scopeRoute selects the scope node for a generated list or create tool.
// A route with at most one eligible parent uses the ordered Chain. A route
// with several eligible parents uses Leaves. The caller supplies exactly one
// leaf reference, and the handler resolves that parent through its chain.
type scopeRoute struct {
	Chain  []ScopeLevel
	Leaves []scopeLeaf
}

// ScopeRouteForType builds a route from the type's CanLiveUnder parents.
//
// Missing parent types and entry point types do not form leaves. The method
// removes duplicate parents and uses ScopeChainForType for ancestor levels.
//
// A comment type under issue and epic can expose these alternative inputs:
//
//   - issue_reference selects the issue parent.
//   - epic_reference selects the epic parent.
func (r *Resolver) ScopeRouteForType(nt *node.NodeType) scopeRoute {
	parents := make([]*node.NodeType, 0, len(nt.CanLiveUnder))
	seen := map[string]struct{}{}

	for _, parentKey := range nt.CanLiveUnder {
		parent := r.typeIndex[parentKey]
		if parent == nil || parent.Features.Has(node.FeatureIsEntryPoint) {
			continue
		}

		if _, ok := seen[parent.TypeKey]; ok {
			continue
		}

		seen[parent.TypeKey] = struct{}{}
		parents = append(parents, parent)
	}

	if len(parents) <= 1 {
		return scopeRoute{Chain: r.ScopeChainForType(nt), Leaves: nil}
	}

	leaves := make([]scopeLeaf, 0, len(parents))
	for _, parent := range parents {
		leaves = append(
			leaves,
			scopeLeaf{
				Level: ScopeLevel{
					TypeKey:   parent.TypeKey,
					Slug:      strings.ToLower(parent.Slug),
					ParamName: strings.ToLower(parent.Slug) + referenceParamSuffix,
				},
				Chain: r.ScopeChainForType(parent),
			},
		)
	}

	return scopeRoute{Chain: nil, Leaves: leaves}
}

// hasLevels reports whether the route requires resolution below the entry
// point.
func (route scopeRoute) hasLevels() bool {
	return len(route.Chain) > 0 || len(route.Leaves) > 0
}

// leafParamNames lists the alternative parent parameters in declared order.
func (route scopeRoute) leafParamNames() []string {
	names := make([]string, 0, len(route.Leaves))
	for _, leaf := range route.Leaves {
		names = append(names, leaf.Level.ParamName)
	}

	return names
}

func (route scopeRoute) isLeafParam(name string) bool {
	return slices.Contains(route.leafParamNames(), name)
}

func (route scopeRoute) exactlyOneMessage() string {
	return "exactly one of " + strings.Join(route.leafParamNames(), ", ") + " is required"
}

// schemaFields returns chain fields for a route without leaves. For a route
// with leaves, it emits each upper parameter once, skips upper levels that
// use leaf parameters, and then emits each leaf parameter once.
func (route scopeRoute) schemaFields(resolver *Resolver) []schemaField {
	if len(route.Leaves) == 0 {
		fields := make([]schemaField, 0, len(route.Chain))
		for _, level := range route.Chain {
			fields = append(fields, scopeReferenceFields(level, resolver)...)
		}

		return fields
	}

	fields := make([]schemaField, 0, len(route.Leaves)*2)
	seen := map[string]struct{}{}

	for _, leaf := range route.Leaves {
		for _, level := range leaf.Chain {
			if _, ok := seen[level.ParamName]; ok || route.isLeafParam(level.ParamName) {
				continue
			}

			seen[level.ParamName] = struct{}{}
			fields = append(fields, scopeReferenceFields(level, resolver)...)
		}
	}

	exactlyOne := " Provide exactly one of " + strings.Join(route.leafParamNames(), ", ") + "."
	for _, leaf := range route.Leaves {
		field := scopeReferenceFields(leaf.Level, resolver)[0]
		field.Desc += exactlyOne
		fields = append(fields, field)
	}

	return fields
}

// requiredParams returns every chain parameter for a route without leaves.
// For a route with leaves, it returns only parameters that every leaf chain
// shares and excludes all leaf parameters.
func (route scopeRoute) requiredParams() []string {
	if len(route.Leaves) == 0 {
		return scopeParamNames(route.Chain)
	}

	counts := map[string]int{}

	for _, leaf := range route.Leaves {
		for _, level := range leaf.Chain {
			counts[level.ParamName]++
		}
	}

	required := make([]string, 0, len(route.Leaves[0].Chain))
	for _, level := range route.Leaves[0].Chain {
		if counts[level.ParamName] == len(route.Leaves) && !route.isLeafParam(level.ParamName) {
			required = append(required, level.ParamName)
		}
	}

	return required
}
