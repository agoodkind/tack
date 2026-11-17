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

func TestResolveTypedNodeIDProjectReference(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	workspace := &node.NodeView{
		ID:       workspaceID,
		OrgID:    orgID,
		NodeType: "workspace",
		Name:     "Main",
		Props:    map[string]json.RawMessage{"slug": mustRaw(t, "main")},
	}
	project := &node.NodeView{
		ID:       projectID,
		OrgID:    orgID,
		NodeType: "project",
		Name:     "Tack",
		Props: map[string]json.RawMessage{
			"identifier": mustRaw(t, "TACK"),
			"parent_id":  mustRaw(t, workspaceID.String()),
		},
	}
	reader := &resolverReader{
		views:      map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project},
		workspaces: []*node.NodeView{workspace},

		parents: map[uuid.UUID]uuid.UUID{projectID: workspaceID},
	}
	repo := &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"project:identifier:\"TACK\"": {
				{
					ID:       projectID,
					OrgID:    orgID,
					NodeType: "project",
					Props: map[string]json.RawMessage{
						"parent_id": mustRaw(t, workspaceID.String()),
					},
				},
			},
			"project:slug:\"tack\"": nil,
		},
	}
	resolver := &Resolver{
		nodes:             repo,
		reader:            reader,
		members:           &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace",
		entryPointSlug:    "workspace",
	}
	projectType := &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		CanLiveUnder: []string{"workspace"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
	}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))
	id, err := resolver.ResolveTypedNodeID(ctx, projectType, "TACK")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID(TACK): %v", err)
	}

	if id != projectID {
		t.Fatalf("ResolveTypedNodeID(TACK): got %s want %s", id, projectID)
	}

	if audit.ScopeFromContext(ctx).OrgID != orgID {
		t.Fatalf(
			"ResolveTypedNodeID(TACK) audit org: got %s want %s",
			audit.ScopeFromContext(ctx).OrgID,
			orgID,
		)
	}
}
