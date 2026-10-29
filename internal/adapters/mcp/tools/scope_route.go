package tools

import (
	"slices"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

// scopeLeaf is one allowed direct parent of a node type, together with the
// scope levels above that parent.
type scopeLeaf struct {
	Level ScopeLevel
	Chain []ScopeLevel
}

// scopeRoute describes how a generated list or create tool reaches its scope
// node. A type with zero or one declared parent uses Chain, the single ordered
// path the tool has always required. A type that declares several parents in
// CanLiveUnder uses Leaves: the caller provides exactly one leaf reference per
// call, and the handler resolves that parent through the leaf's own chain.
type scopeRoute struct {
	Chain  []ScopeLevel
	Leaves []scopeLeaf
}

// ScopeRouteForType builds the scope route for a node type from its
// CanLiveUnder declaration. Parents missing from the type index and parents
// that are the entry point contribute no leaf, matching ScopeChainForType.
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
		leaves = append(leaves, scopeLeaf{
			Level: ScopeLevel{
				TypeKey:   parent.TypeKey,
				Slug:      strings.ToLower(parent.Slug),
				ParamName: strings.ToLower(parent.Slug) + referenceParamSuffix,
			},
			Chain: r.ScopeChainForType(parent),
		})
	}
	return scopeRoute{Chain: nil, Leaves: leaves}
}

// hasLevels reports whether the route requires resolving below the entry point.
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

// isLeafParam reports whether name matches a leaf's Level.ParamName.
func (route scopeRoute) isLeafParam(name string) bool {
	return slices.Contains(route.leafParamNames(), name)
}

func (route scopeRoute) exactlyOneMessage() string {
	return "exactly one of " + strings.Join(route.leafParamNames(), ", ") + " is required"
}

// schemaFields returns chain fields when the route does not have leaves.
// For routes with leaves, it emits each leaf parameter once and each upper parameter
// once, skipping upper levels that use leaf parameters.
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

// requiredParams returns every chain parameter when the route does not have leaves.
// For routes with leaves, it returns only parameters shared by every leaf chain
// and excludes all leaf parameters.
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
