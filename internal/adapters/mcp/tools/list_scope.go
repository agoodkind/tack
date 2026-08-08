package tools

import "goodkind.io/tack/internal/domain/node"

func listScopeProperty(nt *node.NodeType, route scopeRoute) string {
	if nt.Features.Has(node.FeatureHasSequenceID) && route.hasLevels() {
		return "scope_id"
	}
	return "parent_id"
}
