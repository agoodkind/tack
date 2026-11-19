package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func listScopeProperty(nt *node.NodeType, route scopeRoute) string {
	if nt.Features.Has(node.FeatureHasSequenceID) && route.hasLevels() {
		return "scope_id"
	}

	return ""
}

func listMatch(nt *node.NodeType, route scopeRoute, target routeTarget) (string, uuid.UUID) {
	if target.hasAlternativeParent() {
		return "", target.Parent.ID
	}

	return listScopeProperty(nt, route), target.Scope.ID
}

func applyListScope(query *node.NodeListQuery, property string, nodeID uuid.UUID) {
	if property == "" {
		query.ByTargetRelation = &node.RelationScan{NodeID: nodeID, RelationType: node.RelChildOf}

		return
	}

	query.ByProperty = &node.PropertyMatch{
		PropName: property,
		Value:    mustRawString(nodeID.String()),
	}
}
