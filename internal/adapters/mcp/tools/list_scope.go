package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const parentIDPropertyName = "parent_id"

func listScopeProperty(nt *node.NodeType, route scopeRoute) string {
	if nt.Features.Has(node.FeatureHasSequenceID) && route.hasLevels() {
		return "scope_id"
	}

	return parentIDPropertyName
}

func listMatch(nt *node.NodeType, route scopeRoute, target routeTarget) (string, uuid.UUID) {
	if target.hasAlternativeParent() {
		return "parent_id", target.Parent.ID
	}

	return listScopeProperty(nt, route), target.Scope.ID
}
