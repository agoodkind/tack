package tools

import (
	"context"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
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

func (route scopeRoute) exactlyOneMessage() string {
	return "exactly one of " + strings.Join(route.leafParamNames(), ", ") + " is required"
}

// schemaFields returns the scope parameters the generated tool exposes. For a
// single-parent route these are the chain levels, unchanged. For a
// multi-parent route these are the union of the leaves' upper chains followed
// by one optional field per leaf.
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
			if _, ok := seen[level.ParamName]; ok {
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

// requiredParams returns the scope parameters that are required for every
// call: the whole chain for a single-parent route, and the parameters shared
// by every leaf's chain for a multi-parent route. Leaf parameters are never
// listed because only one of them may be provided.
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
		if counts[level.ParamName] == len(route.Leaves) {
			required = append(required, level.ParamName)
		}
	}
	return required
}

// resolveRouteScope resolves the scope node for one call. A non-nil result is
// an early return the handler must pass back to the client unchanged.
func resolveRouteScope(
	ctx context.Context,
	b NodeTypeBinding,
	ws *node.NodeView,
	route scopeRoute,
	args argMap,
) (*node.NodeView, *mcpmcp.CallToolResult) {
	levels := route.Chain
	if len(route.Leaves) > 0 {
		var chosen *scopeLeaf
		for i := range route.Leaves {
			if optionalScopeReference(args, route.Leaves[i].Level) == "" {
				continue
			}
			if chosen != nil {
				return nil, recoverableError(route.exactlyOneMessage())
			}
			chosen = &route.Leaves[i]
		}
		if chosen == nil {
			return nil, recoverableError(route.exactlyOneMessage())
		}
		levels = append(append([]ScopeLevel{}, chosen.Chain...), chosen.Level)
	}
	parent := ws
	for _, level := range levels {
		reference, ok := requireScopeReference(args, level)
		if !ok {
			return nil, recoverableError(scopeReferenceRequiredMessage(level))
		}
		var err error
		parent, err = b.Resolver.ResolveScope(ctx, parent, level, reference)
		if err != nil {
			return nil, classifyError(ctx, err)
		}
	}
	return parent, nil
}
