package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type sequenceNodeRepo struct {
	*fakeNodeRepo
	holders map[string]uuid.UUID
	lookupN int
	listN   int
}

func (r *sequenceNodeRepo) LookupReference(_ context.Context, _ uuid.UUID, templateName, encoded string) (uuid.UUID, error) {
	r.lookupN++
	return r.holders[templateName+":"+encoded], nil
}

func (r *sequenceNodeRepo) ListByProperty(ctx context.Context, orgID uuid.UUID, nodeType, propName string, value json.RawMessage) ([]*node.Node, error) {
	r.listN++
	return r.fakeNodeRepo.ListByProperty(ctx, orgID, nodeType, propName, value)
}

func sequenceTemplate() node.ReferenceTemplate {
	return node.ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Parts: []node.ReferencePart{
			{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
			{Kind: node.ReferencePartLiteral, Value: "-"},
			{Kind: node.ReferencePartProperty, Value: "sequence"},
		},
	}
}

func TestResolveSequenceNodeIDFailsLoudOnDuplicates(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "FAN"), "parent_id": mustRaw(t, workspaceID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project}, workspaces: []*node.NodeView{workspace}}
	repo := &sequenceNodeRepo{fakeNodeRepo: &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"FAN\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
		"issue:sequence:13": {
			{ID: firstID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())}},
			{ID: secondID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())}},
		},
	}}, holders: map[string]uuid.UUID{}}
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}, ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()}}
	resolver := &Resolver{nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}}, entryPointTypeKey: "workspace", entryPointSlug: "workspace", scopeChain: []ScopeLevel{{TypeKey: "project", Slug: "project", ParamName: "project_reference"}}, typeIndex: map[string]*node.NodeType{"project": projectType, "issue": issueType}}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))

	_, err := resolver.ResolveTypedNodeID(ctx, issueType, "FAN-13")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("ResolveTypedNodeID error = %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), firstID.String()) || !strings.Contains(err.Error(), secondID.String()) {
		t.Fatalf("ResolveTypedNodeID error = %v, want both UUIDs", err)
	}
}

func TestResolveSequenceNodeIDUsesReferenceKeyBeforeScan(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	holderID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace}, workspaces: []*node.NodeView{workspace}}
	repo := &sequenceNodeRepo{fakeNodeRepo: &fakeNodeRepo{}, holders: map[string]uuid.UUID{"reference:FAN-13": holderID}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}, ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()}}
	resolver := &Resolver{nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}}, entryPointTypeKey: "workspace", entryPointSlug: "workspace", typeIndex: map[string]*node.NodeType{"issue": issueType}}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))

	gotID, err := resolver.ResolveTypedNodeID(ctx, issueType, "FAN-13")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID: %v", err)
	}
	if gotID != holderID {
		t.Fatalf("ResolveTypedNodeID = %s, want %s", gotID, holderID)
	}
	if repo.lookupN != 1 {
		t.Fatalf("LookupReference calls = %d, want 1", repo.lookupN)
	}
	if repo.listN != 0 {
		t.Fatalf("ListByProperty calls = %d, want 0", repo.listN)
	}
}
