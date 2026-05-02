package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

func TestRenderAuditRowsShowsCorrelation(t *testing.T) {
	contextJSON, err := json.Marshal(audit.EventContext{
		RequestID: "req-render",
		TraceID:   "trace-render",
		Source:    audit.SourceMCP,
	})
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	row := audit.Row{
		EventTime:  time.Unix(10, 0).UTC(),
		ActorID:    uuid.Must(uuid.NewV7()),
		Action:     "node.read",
		EntityKind: "node",
		EntityID:   uuid.Must(uuid.NewV7()),
		Context:    contextJSON,
		Seq:        1,
	}

	out := renderAuditRows([]audit.Row{row})

	for _, want := range []string{"request", "trace", "req-render", "trace-render"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered audit rows missing %q:\n%s", want, out)
		}
	}
}
func TestStampAuditNodeInEntryPointPopulatesWorkspace(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}
	issue := &node.NodeView{ID: issueID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, projectID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{projectID: project}}
	resolver := &Resolver{reader: reader}
	ctx := audit.WithScopeBuilder(context.Background())

	if err := stampAuditNodeInEntryPoint(ctx, resolver, workspace, issue); err != nil {
		t.Fatalf("stampAuditNodeInEntryPoint: %v", err)
	}
	scope := audit.ScopeFromContext(ctx)
	if scope.OrgID != orgID {
		t.Fatalf("audit org: got %s want %s", uuid.UUID(scope.OrgID), orgID)
	}
	if scope.WorkspaceID != workspaceID {
		t.Fatalf("audit workspace: got %s want %s", uuid.UUID(scope.WorkspaceID), workspaceID)
	}
}

func TestResolveTypedNodeIDSequenceStampsWorkspace(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "TACK"), "parent_id": mustRaw(t, workspaceID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"TACK\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
		"issue:sequence:65":           {{ID: issueID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())}}},
	}}
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}}
	resolver := &Resolver{
		nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace", entryPointSlug: "workspace",
		scopeChain: []ScopeLevel{{TypeKey: "project", Slug: "project", ParamName: "project_identifier"}},
		typeIndex:  map[string]*node.NodeType{"project": projectType, "issue": issueType},
	}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))

	id, err := resolver.ResolveTypedNodeID(ctx, issueType, "TACK-65")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID(TACK-65): %v", err)
	}
	if id != issueID {
		t.Fatalf("ResolveTypedNodeID(TACK-65): got %s want %s", id, issueID)
	}
	scope := audit.ScopeFromContext(ctx)
	if scope.WorkspaceID != workspaceID {
		t.Fatalf("audit workspace: got %s want %s", uuid.UUID(scope.WorkspaceID), workspaceID)
	}
}
