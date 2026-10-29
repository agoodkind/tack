package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// routeTarget pairs the resolved Scope with the create Parent.
// ParentParam identifies the chosen leaf parameter when the route has leaves.
// Scope is an ancestor of Parent when the leaf chain has an alternative parent level.
type routeTarget struct {
	Scope       *node.NodeView
	Parent      *node.NodeView
	ParentParam string
}

// hasAlternativeParent reports whether Parent and Scope have different IDs.
func (target routeTarget) hasAlternativeParent() bool {
	return target.Parent.ID != target.Scope.ID
}

// refuseParentProperty returns a recoverable error when props contains parent_id
// and Parent and Scope have different IDs.
func (target routeTarget) refuseParentProperty(props map[string]json.RawMessage) *mcpmcp.CallToolResult {
	if !target.hasAlternativeParent() {
		return nil
	}
	if _, present := props["parent_id"]; !present {
		return nil
	}
	return recoverableError("Specify the parent with exactly one of " + target.ParentParam + " or properties.parent_id.")
}

// createParentID returns Parent.ID when Parent and Scope have different IDs.
// It returns normalizedParentID otherwise.
func (target routeTarget) createParentID(normalizedParentID uuid.UUID) uuid.UUID {
	if target.hasAlternativeParent() {
		return target.Parent.ID
	}
	return normalizedParentID
}

// chooseLeaf returns a leaf when exactly one leaf has a nonempty scope reference,
// or nil otherwise.
func (route scopeRoute) chooseLeaf(args argMap) *scopeLeaf {
	var chosen *scopeLeaf
	for i := range route.Leaves {
		if optionalScopeReference(args, route.Leaves[i].Level) == "" {
			continue
		}
		if chosen != nil {
			return nil
		}
		chosen = &route.Leaves[i]
	}
	return chosen
}

// splitChain returns the chosen leaf's chain without levels that use leaf parameters.
// It reports whether it skipped any such alternative parent levels.
func (route scopeRoute) splitChain(chosen *scopeLeaf) ([]ScopeLevel, bool) {
	upper := make([]ScopeLevel, 0, len(chosen.Chain)+1)
	hasAlternative := false
	for _, level := range chosen.Chain {
		if route.isLeafParam(level.ParamName) {
			hasAlternative = true
			continue
		}
		upper = append(upper, level)
	}
	return upper, hasAlternative
}

// resolveLevels resolves each required scope reference from start in level order.
// It returns start when levels is empty and a tool error if a reference is missing
// or resolution fails.
func resolveLevels(
	ctx context.Context,
	b NodeTypeBinding,
	start *node.NodeView,
	levels []ScopeLevel,
	args argMap,
) (*node.NodeView, *mcpmcp.CallToolResult) {
	current := start
	for _, level := range levels {
		reference, ok := requireScopeReference(args, level)
		if !ok {
			return nil, recoverableError(scopeReferenceRequiredMessage(level))
		}
		var err error
		current, err = b.Resolver.ResolveScope(ctx, current, level, reference)
		if err != nil {
			return nil, classifyError(ctx, err)
		}
	}
	return current, nil
}

// resolveRouteScope resolves Scope and Parent and requires exactly one leaf when
// route has leaves. If the leaf chain has an alternative parent level, it derives
// Scope from Parent's ancestor at the deepest chain level.
func resolveRouteScope(
	ctx context.Context,
	b NodeTypeBinding,
	ws *node.NodeView,
	route scopeRoute,
	args argMap,
) (routeTarget, *mcpmcp.CallToolResult) {
	if len(route.Leaves) == 0 {
		scope, early := resolveLevels(ctx, b, ws, route.Chain, args)
		if early != nil {
			return routeTarget{Scope: nil, Parent: nil, ParentParam: ""}, early
		}
		return routeTarget{Scope: scope, Parent: scope, ParentParam: ""}, nil
	}
	chosen := route.chooseLeaf(args)
	if chosen == nil {
		return routeTarget{Scope: nil, Parent: nil, ParentParam: ""}, recoverableError(route.exactlyOneMessage())
	}
	upper, hasAlternative := route.splitChain(chosen)
	parent, early := resolveLevels(ctx, b, ws, append(upper, chosen.Level), args)
	if early != nil {
		return routeTarget{Scope: nil, Parent: nil, ParentParam: ""}, early
	}
	target := routeTarget{Scope: parent, Parent: parent, ParentParam: chosen.Level.ParamName}
	if !hasAlternative {
		return target, nil
	}
	scope, err := b.Resolver.ancestorOfType(ctx, parent, chosen.Chain[len(chosen.Chain)-1])
	if err != nil {
		return routeTarget{Scope: nil, Parent: nil, ParentParam: ""}, classifyError(ctx, err)
	}
	audit.SetScopeFields(ctx, audit.Scope{OrgID: uuid.Nil, WorkspaceID: uuid.Nil, ScopeID: scope.ID, ParentID: uuid.Nil})
	target.Scope = scope
	return target, nil
}

// ancestorOfType returns the first parent_id ancestor with level.TypeKey.
// It returns an error if a read fails or the parent chain ends or cycles without a match.
func (r *Resolver) ancestorOfType(ctx context.Context, start *node.NodeView, level ScopeLevel) (*node.NodeView, error) {
	visited := map[uuid.UUID]struct{}{start.ID: {}}
	currentID := uuidProp(start, "parent_id")
	for currentID != uuid.Nil {
		if _, seen := visited[currentID]; seen {
			break
		}
		visited[currentID] = struct{}{}
		view, err := r.reader.Get(ctx, currentID)
		if err != nil {
			return nil, fmt.Errorf("get ancestor %s of %s: %w", currentID, start.ID, err)
		}
		if view == nil {
			break
		}
		if view.NodeType == level.TypeKey {
			return view, nil
		}
		currentID = uuidProp(view, "parent_id")
	}
	err := fmt.Errorf("%s %s does not have a %s ancestor: %w", start.NodeType, start.ID, level.Slug, domain.ErrNotFound)
	slog.WarnContext(ctx, "resolver.scope.ancestor_missing",
		slog.String("node_id", start.ID.String()), slog.String("ancestor_type", level.TypeKey), slog.String("err", err.Error()))
	return nil, err
}
