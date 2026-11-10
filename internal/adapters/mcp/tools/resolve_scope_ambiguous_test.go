package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// A scope reference held by two nodes under the same parent is reported as
// ambiguous with both candidates, not as not found (TACK-476).
func TestResolveScopeReportsAmbiguousReferenceWithCandidates(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	first := uuid.New()
	second := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	parentProps := map[string]json.RawMessage{"identifier": mustRaw(t, "Q0101"), "parent_id": mustRaw(t, workspaceID.String())}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{
		workspaceID: workspace,
		first:       {ID: first, OrgID: orgID, NodeType: "project", Name: "One", Props: parentProps},
		second:      {ID: second, OrgID: orgID, NodeType: "project", Name: "Two", Props: parentProps},
	}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"Q0101\"": {
			{ID: first, OrgID: orgID, NodeType: "project", Props: parentProps},
			{ID: second, OrgID: orgID, NodeType: "project", Props: parentProps},
		},
	}}
	projectType := &node.NodeType{
		TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}
	resolver := &Resolver{
		nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace", entryPointSlug: "workspace",
		typeIndex: map[string]*node.NodeType{"project": projectType},
	}
	ctx := auth.WithUser(context.Background(), uuid.New())

	_, err := resolver.ResolveScope(ctx, workspace, ScopeLevel{TypeKey: "project", Slug: "project", ParamName: "project_reference"}, "Q0101")
	if err == nil {
		t.Fatal("ResolveScope resolved an ambiguous reference")
	}
	if !errors.Is(err, domain.ErrInvalidArgument) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want invalid argument, not not-found", err)
	}
	if !strings.Contains(err.Error(), first.String()) || !strings.Contains(err.Error(), second.String()) {
		t.Fatalf("err = %v, want both candidates named", err)
	}
}
