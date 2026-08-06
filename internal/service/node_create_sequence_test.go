package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateStampsTemplateGeneratedProperty(t *testing.T) {
	service, repo, parentID := templateSequenceService(t, "ticket", []node.ReferenceTemplate{{
		Name:      "reference",
		IsPrimary: true,
		Generated: "number",
		Parts: []node.ReferencePart{
			{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
			{Kind: node.ReferencePartLiteral, Value: "-"},
			{Kind: node.ReferencePartProperty, Value: "number"},
		},
	}})

	result, err := service.Create(context.Background(), CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: "ticket", Name: "One", ActorID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if string(result.View.Props["number"]) != "1" {
		t.Fatalf("number = %s, want 1", result.View.Props["number"])
	}
	if _, exists := result.View.Props["sequence"]; exists {
		t.Fatal("sequence property was stamped")
	}
	if len(repo.counterKeys) != 1 || repo.counterKeys[0] != "FAN-" {
		t.Fatalf("counter keys = %#v, want [FAN-]", repo.counterKeys)
	}
}

func TestCreateWithoutTemplateSkipsSequenceAllocation(t *testing.T) {
	service, repo, parentID := templateSequenceService(t, "plain", nil)

	result, err := service.Create(context.Background(), CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: "plain", Name: "Plain", ActorID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, exists := result.View.Props["number"]; exists {
		t.Fatal("number property was stamped")
	}
	if _, exists := result.View.Props["sequence"]; exists {
		t.Fatal("sequence property was stamped")
	}
	if len(repo.counterKeys) != 0 {
		t.Fatalf("counter keys = %#v, want none", repo.counterKeys)
	}
}

func TestCreateSharesCounterForMatchingTemplateKeys(t *testing.T) {
	service, repo, parentID := templateSequenceService(t, "issue", sharedCounterTemplate())
	service.nodeTypes = &idempotencyTypes{types: []*node.NodeType{
		templateScopeNodeType(),
		{TypeKey: "issue", ReferenceTemplates: sharedCounterTemplate()},
		{TypeKey: "epic", ReferenceTemplates: sharedCounterTemplate()},
	}}

	issue, err := service.Create(context.Background(), CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: "issue", Name: "Issue", ActorID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	epic, err := service.Create(context.Background(), CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: "epic", Name: "Epic", ActorID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	if string(issue.View.Props["number"]) != "1" || string(epic.View.Props["number"]) != "2" {
		t.Fatalf("numbers = %s, %s, want 1, 2", issue.View.Props["number"], epic.View.Props["number"])
	}
	if len(repo.counterKeys) != 2 || repo.counterKeys[0] != "FAN-" || repo.counterKeys[1] != "FAN-" {
		t.Fatalf("counter keys = %#v, want matching FAN- keys", repo.counterKeys)
	}
}

func templateSequenceService(t *testing.T, typeKey string, templates []node.ReferenceTemplate) (*NodeService, *idempotencyNodeRepo, uuid.UUID) {
	t.Helper()
	orgID := uuid.New()
	parentID := uuid.New()
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{
		parentID: {ID: parentID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"identifier": json.RawMessage(`"FAN"`)}},
	}}
	types := []*node.NodeType{templateScopeNodeType(), {TypeKey: typeKey, ReferenceTemplates: templates}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}, {Name: "number"}}
	return NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{}), repo, parentID
}

func templateScopeNodeType() *node.NodeType {
	return &node.NodeType{
		TypeKey:   "project",
		Features:  node.Features{node.FeatureIsScope},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}
}

func sharedCounterTemplate() []node.ReferenceTemplate {
	return []node.ReferenceTemplate{{
		Name:      "reference",
		IsPrimary: true,
		Generated: "number",
		Parts: []node.ReferencePart{
			{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
			{Kind: node.ReferencePartLiteral, Value: "-"},
			{Kind: node.ReferencePartProperty, Value: "number"},
		},
	}}
}
