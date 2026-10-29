package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func listScopeProperty(nt *node.NodeType, route scopeRoute) string {
	if nt.Features.Has(node.FeatureHasSequenceID) && route.hasLevels() {
		return "scope_id"
	}
	return "parent_id"
}

// listMatch returns parent_id and Parent.ID when Parent and Scope have different IDs.
// Otherwise, it returns the property from listScopeProperty and Scope.ID.
func listMatch(nt *node.NodeType, route scopeRoute, target routeTarget) (string, uuid.UUID) {
	if target.hasAlternativeParent() {
		return "parent_id", target.Parent.ID
	}
	return listScopeProperty(nt, route), target.Scope.ID
}
