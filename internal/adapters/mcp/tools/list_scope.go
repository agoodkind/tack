package tools

import "goodkind.io/tack/internal/domain/node"

func listScopeProperty(nt *node.NodeType, chain []ScopeLevel) string {
	if nt.Features.Has(node.FeatureHasSequenceID) && len(chain) > 0 {
		return "scope_id"
	}
	return "parent_id"
}
