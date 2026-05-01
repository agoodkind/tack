package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
)

type fakeMembers struct {
	orgIDs []uuid.UUID
}

func (m *fakeMembers) AddMember(context.Context, *org.Member) error {
	panic("fakeMembers.AddMember called")
}
func (m *fakeMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	panic("fakeMembers.RemoveMember called")
}
func (m *fakeMembers) ListMembers(context.Context, uuid.UUID) ([]*org.Member, error) {
	panic("fakeMembers.ListMembers called")
}
func (m *fakeMembers) ListOrgIDsForUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return m.orgIDs, nil
}

type resolverReader struct {
	views      map[uuid.UUID]*node.NodeView
	workspaces []*node.NodeView
}

func (r *resolverReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}
func (r *resolverReader) Resolve(_ context.Context, id uuid.UUID) (*node.NodeResolve, error) {
	view := r.views[id]
	if view == nil {
		return nil, domain.ErrNotFound
	}
	return &node.NodeResolve{OrgID: view.OrgID, NodeType: view.NodeType}, nil
}
func (r *resolverReader) List(_ context.Context, q node.NodeListQuery) ([]*node.NodeView, error) {
	if q.NodeType == "workspace" && q.ByProperty == nil {
		return r.workspaces, nil
	}
	if q.ByProperty == nil {
		return nil, nil
	}
	out := make([]*node.NodeView, 0)
	for _, view := range r.views {
		if view.NodeType != q.NodeType {
			continue
		}
		if string(view.Props[q.ByProperty.PropName]) != string(q.ByProperty.Value) {
			continue
		}
		out = append(out, view)
	}
	return out, nil
}
func (r *resolverReader) Stream(context.Context, node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	panic("resolverReader.Stream called")
}

type fakeNodeRepo struct {
	scopeChildren map[string][]*node.Node
}

func (r *fakeNodeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	panic("fakeNodeRepo.Get called")
}
func (r *fakeNodeRepo) Set(context.Context, *node.Node, *node.NodeView) error {
	panic("fakeNodeRepo.Set called")
}
func (r *fakeNodeRepo) UpdateAtomic(context.Context, *node.Node, *node.NodeView, map[string]json.RawMessage, []string) error {
	panic("fakeNodeRepo.UpdateAtomic called")
}
func (r *fakeNodeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("fakeNodeRepo.Delete called")
}
func (r *fakeNodeRepo) CreateAtomic(context.Context, *node.Node, *node.NodeView, []*node.Relationship, []string) error {
	panic("fakeNodeRepo.CreateAtomic called")
}
func (r *fakeNodeRepo) ListByProperty(_ context.Context, _ uuid.UUID, nodeType, propName string, value json.RawMessage) ([]*node.Node, error) {
	return r.scopeChildren[nodeType+":"+propName+":"+string(value)], nil
}
func (r *fakeNodeRepo) AllocateSequence(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	panic("fakeNodeRepo.AllocateSequence called")
}
func (r *fakeNodeRepo) GetSlug(context.Context, string, string) (uuid.UUID, error) {
	panic("fakeNodeRepo.GetSlug called")
}
func (r *fakeNodeRepo) WriteSlug(context.Context, string, string, uuid.UUID) error {
	panic("fakeNodeRepo.WriteSlug called")
}
func (r *fakeNodeRepo) DeleteSlug(context.Context, string, string) error {
	panic("fakeNodeRepo.DeleteSlug called")
}
func (r *fakeNodeRepo) LookupIdempotencyKey(context.Context, uuid.UUID, string) (uuid.UUID, error) {
	panic("fakeNodeRepo.LookupIdempotencyKey called")
}
func (r *fakeNodeRepo) WriteIdempotencyKey(context.Context, uuid.UUID, string, uuid.UUID) error {
	panic("fakeNodeRepo.WriteIdempotencyKey called")
}

func TestResolveTypedNodeIDProjectIdentifier(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Tack", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "TACK"), "parent_id": mustRaw(t, workspaceID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"TACK\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
		"project:slug:\"tack\"":       nil,
	}}
	resolver := &Resolver{nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}}, entryPointTypeKey: "workspace", entryPointSlug: "workspace"}
	projectType := &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		CanLiveUnder: []string{"workspace"},
		Reference:    node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"},
	}
	ctx := auth.WithUser(context.Background(), uuid.New())
	id, err := resolver.ResolveTypedNodeID(ctx, projectType, "TACK")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID(TACK): %v", err)
	}
	if id != projectID {
		t.Fatalf("ResolveTypedNodeID(TACK): got %s want %s", id, projectID)
	}
}

func TestResolveTypedNodeIDScopedStateReference(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Clyde", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "CLYDE"), "parent_id": mustRaw(t, workspaceID.String())}}
	state := &node.NodeView{ID: stateID, OrgID: orgID, NodeType: "state", Name: "In Progress", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, projectID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project, stateID: state}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"CLYDE\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
	}}
	resolver := &Resolver{nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}}, entryPointTypeKey: "workspace", entryPointSlug: "workspace", typeIndex: map[string]*node.NodeType{"project": {TypeKey: "project", Slug: "project"}}}
	stateType := &node.NodeType{
		TypeKey:      "state",
		Slug:         "state",
		CanLiveUnder: []string{"project"},
		Reference:    node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"},
	}
	ctx := auth.WithUser(context.Background(), uuid.New())
	id, err := resolver.ResolveTypedNodeID(ctx, stateType, "CLYDE::In Progress")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID(CLYDE::In Progress): %v", err)
	}
	if id != stateID {
		t.Fatalf("ResolveTypedNodeID(CLYDE::In Progress): got %s want %s", id, stateID)
	}
	_, err = resolver.ResolveTypedNodeID(ctx, stateType, "In Progress")
	if err == nil || !strings.Contains(err.Error(), domain.ErrInvalidArgument.Error()) {
		t.Fatalf("ResolveTypedNodeID(In Progress): got %v want invalid argument", err)
	}
}
