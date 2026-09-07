package tools

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestCreateToolCommitsItsRowWithTheNodeAndRecordsItOnce is TACK-173 at the
// tool boundary. When the store commits the staged row with the node, the
// wrapper records the transport row and nothing else, so the ledger holds one
// node.create for the call rather than two; the staged row itself names the
// created node, the caller, and the tool.
func TestCreateToolCommitsItsRowWithTheNodeAndRecordsItOnce(t *testing.T) {
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
		TypeKey: "workspace", Slug: "workspace",
		Features:  node.Features{node.FeatureIsEntryPoint, node.FeatureIsScope},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"},
	}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"workspace"}}
	workspace := &node.NodeView{
		ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main",
		Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")},
	}
	reader := &resolverReader{
		views:      map[uuid.UUID]*node.NodeView{workspaceID: workspace},
		workspaces: []*node.NodeView{workspace},
	}
	repo := &intentAuditNodeRepo{fakeNodeRepo: &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"workspace:slug:\"main\"": {{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}},
		},
	}}
	nodeTypes := []*node.NodeType{workspaceType, issueType}
	resolver := NewResolver(repo, reader, &fakeMembers{orgIDs: []uuid.UUID{orgID}}, nodeTypes)
	propertyDefs := &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}}}
	nodeService := service.NewNodeService(
		repo, reader, &createAuditTypes{types: nodeTypes}, propertyDefs, nil, nil, createAuditSearcher{},
	)
	binding := NodeTypeBinding{NodeSvc: nodeService, Reader: reader, PropertyDefs: propertyDefs, Resolver: resolver}
	handler := wrapToolHandler(
		"tack_create_issue",
		createHandler(issueType, resolver.ScopeRouteForType(issueType), binding),
	)

	result, err := handler(auth.WithUser(t.Context(), actorID), callToolReq("tack_create_issue", map[string]any{
		"workspace_reference": "main",
		"name":                "Committed issue",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("create result: %s", resultText(t, result))
	}
	if repo.created == nil || repo.written == nil {
		t.Fatal("the store must receive the staged row with the node")
	}

	var staged audit.Event
	if err := json.Unmarshal(repo.written, &staged); err != nil {
		t.Fatalf("decode the staged row: %v", err)
	}
	if staged.Verb != string(audit.VerbNodeCreate) || staged.Entity.ID != repo.created.ID {
		t.Fatalf("staged = %+v, want node.create of %s", staged, repo.created.ID)
	}
	if staged.Actor.ID != actorID || staged.Context.Tool != "tack_create_issue" || staged.Context.OrgID != orgID {
		t.Fatalf("staged = %+v, want the caller, the tool, and the resolved org", staged)
	}
	assertAuditEvent(t, recorder.Events(), string(audit.VerbMCPToolInvoked), actorID, "mcp_tool", "tack_create_issue")
	for _, event := range recorder.Events() {
		if event.Verb == string(audit.VerbNodeCreate) {
			t.Fatalf("the wrapper recorded node.create again after the transaction carried it: %+v", event)
		}
	}
}
