package tools

import (
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

func TestListScopePropertyUsesScopeForSequenceTypes(t *testing.T) {
	nodeType := &node.NodeType{
		Features: node.Features{node.FeatureHasSequenceID},
	}
	got := listScopeProperty(nodeType, scopeRoute{Chain: []ScopeLevel{{TypeKey: "project"}}})
	if got != "scope_id" {
		t.Fatalf("listScopeProperty: got %q want scope_id", got)
	}
}

func TestListScopePropertyUsesParentForDirectChildren(t *testing.T) {
	nodeType := &node.NodeType{}
	got := listScopeProperty(nodeType, scopeRoute{Chain: []ScopeLevel{{TypeKey: "project"}}})
	if got != "parent_id" {
		t.Fatalf("listScopeProperty: got %q want parent_id", got)
	}
}
