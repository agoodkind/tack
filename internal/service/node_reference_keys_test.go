package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestScopeRefsForRecordsEveryAncestorFeature(t *testing.T) {
	orgID := uuid.New()
	scopeID := uuid.New()
	service := referenceKeyService(orgID, []*node.NodeType{{
		TypeKey:   "scope",
		Features:  node.Features{"feature_a", "feature_b"},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}}, map[uuid.UUID]*node.NodeView{
		scopeID: {ID: scopeID, NodeType: "scope", Props: map[string]json.RawMessage{"identifier": json.RawMessage(`"FAN"`)}},
	})

	scopeRefs, err := service.scopeRefsFor(context.Background(), orgID, scopeID)
	if err != nil {
		t.Fatalf("scopeRefsFor: %v", err)
	}
	if scopeRefs["feature_a"] != "FAN" || scopeRefs["feature_b"] != "FAN" {
		t.Fatalf("scope refs = %#v, want FAN for both features", scopeRefs)
	}
}

func TestScopeRefsForKeepsNearestAncestor(t *testing.T) {
	orgID := uuid.New()
	nearID := uuid.New()
	farID := uuid.New()
	service := referenceKeyService(orgID, []*node.NodeType{{
		TypeKey:   "scope",
		Features:  node.Features{"feature"},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}}, map[uuid.UUID]*node.NodeView{
		nearID: {ID: nearID, NodeType: "scope", Props: map[string]json.RawMessage{"identifier": json.RawMessage(`"NEAR"`), "parent_id": json.RawMessage(`"` + farID.String() + `"`)}},
		farID:  {ID: farID, NodeType: "scope", Props: map[string]json.RawMessage{"identifier": json.RawMessage(`"FAR"`)}},
	})

	scopeRefs, err := service.scopeRefsFor(context.Background(), orgID, nearID)
	if err != nil {
		t.Fatalf("scopeRefsFor: %v", err)
	}
	if scopeRefs["feature"] != "NEAR" {
		t.Fatalf("scope reference = %q, want NEAR", scopeRefs["feature"])
	}
}

func TestScopeRefsForNilScopeReturnsEmptyMap(t *testing.T) {
	service := referenceKeyService(uuid.New(), nil, nil)
	scopeRefs, err := service.scopeRefsFor(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatalf("scopeRefsFor: %v", err)
	}
	if len(scopeRefs) != 0 {
		t.Fatalf("scope refs = %#v, want empty", scopeRefs)
	}
}

func TestReferenceKeysForReturnsNilWithoutTemplates(t *testing.T) {
	service := referenceKeyService(uuid.New(), nil, nil)
	keys, err := service.referenceKeysFor(context.Background(), uuid.New(), &node.NodeType{TypeKey: "plain"}, uuid.Nil, nil)
	if err != nil {
		t.Fatalf("referenceKeysFor: %v", err)
	}
	if keys != nil {
		t.Fatalf("reference keys = %#v, want nil", keys)
	}
}

func TestReferenceKeysForRendersEveryTemplate(t *testing.T) {
	service := referenceKeyService(uuid.New(), nil, nil)
	nodeType := &node.NodeType{TypeKey: "work", ReferenceTemplates: []node.ReferenceTemplate{
		{Name: "one", Parts: []node.ReferencePart{{Kind: node.ReferencePartProperty, Value: "first"}}},
		{Name: "two", Parts: []node.ReferencePart{{Kind: node.ReferencePartProperty, Value: "second"}}},
	}}
	keys, err := service.referenceKeysFor(context.Background(), uuid.New(), nodeType, uuid.Nil, map[string]json.RawMessage{
		"first":  json.RawMessage(`"one"`),
		"second": json.RawMessage(`"two"`),
	})
	if err != nil {
		t.Fatalf("referenceKeysFor: %v", err)
	}
	if len(keys) != 2 || keys[0].Encoded != "one" || keys[1].Encoded != "two" {
		t.Fatalf("reference keys = %#v", keys)
	}
}

func TestReferenceKeysForNamesNodeTypeOnRenderFailure(t *testing.T) {
	service := referenceKeyService(uuid.New(), nil, nil)
	nodeType := &node.NodeType{TypeKey: "work", ReferenceTemplates: []node.ReferenceTemplate{{
		Name: "reference", Parts: []node.ReferencePart{{Kind: node.ReferencePartProperty, Value: "missing"}},
	}}}
	_, err := service.referenceKeysFor(context.Background(), uuid.New(), nodeType, uuid.Nil, nil)
	if err == nil || !strings.Contains(err.Error(), "work") {
		t.Fatalf("referenceKeysFor error = %v, want node type name", err)
	}
}

func referenceKeyService(orgID uuid.UUID, types []*node.NodeType, views map[uuid.UUID]*node.NodeView) *NodeService {
	return &NodeService{
		reader:    &idempotencyReader{orgID: orgID, views: views},
		nodeTypes: &idempotencyTypes{types: types},
	}
}
