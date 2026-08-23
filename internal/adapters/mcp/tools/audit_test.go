package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

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
		t.Fatalf("audit org: got %s want %s", scope.OrgID, orgID)
	}
	if scope.WorkspaceID != workspaceID {
		t.Fatalf("audit workspace: got %s want %s", scope.WorkspaceID, workspaceID)
	}
}

// TestResolveTypedNodeIDSequenceStampsOrg pins the audit stamping on the
// point-read sequence path: the resolution stamps the holder's org. It stamps
// no workspace, because the uniqueness index maps a reference straight to its
// holder and no scope walk identifies one; the earlier workspace stamp was a
// byproduct of the removed property-scan fallback.
func TestResolveTypedNodeIDSequenceStampsOrg(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	issueID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace}, workspaces: []*node.NodeView{workspace}}
	repo := &sequenceNodeRepo{fakeNodeRepo: &fakeNodeRepo{}, holders: map[string]uuid.UUID{"reference:TACK-65": issueID}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}, ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()}}
	resolver := &Resolver{
		nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace", entryPointSlug: "workspace",
		typeIndex: map[string]*node.NodeType{"issue": issueType},
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
	if scope.OrgID != orgID {
		t.Fatalf("audit org: got %s want %s", scope.OrgID, orgID)
	}
	if scope.WorkspaceID != uuid.Nil {
		t.Fatalf("audit workspace: got %s, want unset; the point read identifies no workspace", scope.WorkspaceID)
	}
}
