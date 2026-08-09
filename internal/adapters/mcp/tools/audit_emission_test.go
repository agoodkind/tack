package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func TestWrappedToolRecordsInvocation(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	previousRecorder := currentAuditRecorder()
	SetAuditRecorder(recorder)
	t.Cleanup(func() {
		SetAuditRecorder(previousRecorder)
	})

	actorID := uuid.New()
	handler := wrapToolHandler("tack_list_workspaces", func(
		context.Context,
		mcpmcp.CallToolRequest,
	) (*mcpmcp.CallToolResult, error) {
		return successText("ok", ""), nil
	})
	_, err := handler(auth.WithUser(t.Context(), actorID), callToolReq("tack_list_workspaces", nil))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	assertAuditEvent(t, recorder.Events(), string(audit.VerbMCPToolInvoked), actorID, "mcp_tool", "tack_list_workspaces")
	assertAuditEvent(t, recorder.Events(), string(audit.VerbWorkspaceList), actorID, "mcp_tool", "tack_list_workspaces")
}

func TestCreateToolRecordsCreatedNode(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	previousRecorder := currentAuditRecorder()
	SetAuditRecorder(recorder)
	t.Cleanup(func() {
		SetAuditRecorder(previousRecorder)
	})

	orgID := uuid.New()
	workspaceID := uuid.New()
	actorID := uuid.New()
	workspaceType := &node.NodeType{
		TypeKey: "workspace",
		Slug:    "workspace",
		Features: node.Features{
			node.FeatureIsEntryPoint,
			node.FeatureIsScope,
		},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "slug",
		},
	}
	issueType := &node.NodeType{
		TypeKey:      "issue",
		Slug:         "issue",
		CanLiveUnder: []string{"workspace"},
	}
	workspace := &node.NodeView{
		ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main",
		Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")},
	}
	reader := &resolverReader{
		views:      map[uuid.UUID]*node.NodeView{workspaceID: workspace},
		workspaces: []*node.NodeView{workspace},
	}
	repo := &createAuditNodeRepo{fakeNodeRepo: &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"workspace:slug:\"main\"": {{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}},
		},
	}}
	nodeTypes := []*node.NodeType{workspaceType, issueType}
	resolver := NewResolver(repo, reader, &fakeMembers{orgIDs: []uuid.UUID{orgID}}, nodeTypes)
	propertyDefs := &fakePropertyDefs{defs: []*node.PropertyDef{
		{Name: "parent_id"},
		{Name: "scope_id"},
	}}
	nodeService := service.NewNodeService(
		repo,
		reader,
		&createAuditTypes{types: nodeTypes},
		propertyDefs,
		nil,
		nil,
		createAuditSearcher{},
	)
	binding := NodeTypeBinding{
		NodeSvc:      nodeService,
		Reader:       reader,
		PropertyDefs: propertyDefs,
		Resolver:     resolver,
	}
	handler := wrapToolHandler(
		"tack_create_issue",
		createHandler(issueType, resolver.ScopeRouteForType(issueType), binding),
	)
	result, err := handler(auth.WithUser(t.Context(), actorID), callToolReq("tack_create_issue", map[string]any{
		"workspace_reference": "main",
		"name":                "Created issue",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("create result: %s", resultText(t, result))
	}
	if repo.created == nil {
		t.Fatal("create handler did not create a node")
	}
	assertNodeAuditEvent(
		t,
		recorder.Events(),
		string(audit.VerbNodeCreate),
		repo.created.ID,
		"issue",
		"Created issue",
		"Created issue",
	)
}

func assertAuditEvent(t *testing.T, events []audit.Event, verb string, actorID uuid.UUID, entityType, entityName string) {
	t.Helper()
	for _, event := range events {
		if event.Verb != verb {
			continue
		}
		if event.Actor.ID != actorID {
			t.Errorf("%s actor = %s, want %s", verb, event.Actor.ID, actorID)
		}
		if event.Entity.Type != entityType {
			t.Errorf("%s entity type = %q, want %q", verb, event.Entity.Type, entityType)
		}
		if event.Entity.Name != entityName {
			t.Errorf("%s entity name = %q, want %q", verb, event.Entity.Name, entityName)
		}
		return
	}
	t.Errorf("missing %s event", verb)
}

func assertNodeAuditEvent(
	t *testing.T,
	events []audit.Event,
	verb string,
	id uuid.UUID,
	nodeType, identifier, name string,
) {
	t.Helper()
	for _, event := range events {
		if event.Verb != verb {
			continue
		}
		if event.Entity.Type != "node" {
			t.Errorf("%s entity type = %q, want node", verb, event.Entity.Type)
		}
		if event.Entity.ID != id {
			t.Errorf("%s entity id = %s, want %s", verb, event.Entity.ID, id)
		}
		if event.Entity.NodeType != nodeType {
			t.Errorf("%s node type = %q, want %q", verb, event.Entity.NodeType, nodeType)
		}
		if event.Entity.Identifier != identifier {
			t.Errorf("%s identifier = %q, want %q", verb, event.Entity.Identifier, identifier)
		}
		if event.Entity.Name != name {
			t.Errorf("%s name = %q, want %q", verb, event.Entity.Name, name)
		}
		return
	}
	t.Errorf("missing %s event", verb)
}
