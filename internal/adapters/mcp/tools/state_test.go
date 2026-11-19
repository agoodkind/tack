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

func TestResolveTypedNodeIDScopedStateReference(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
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
		Name:     "Clyde",
		Props: map[string]json.RawMessage{
			"identifier": mustRaw(t, "CLYDE"),
		},
	}
	state := &node.NodeView{
		ID:       stateID,
		OrgID:    orgID,
		NodeType: "state",
		Name:     "In Progress",
	}
	reader := &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			workspaceID: workspace,
			projectID:   project,
			stateID:     state,
		},
		workspaces: []*node.NodeView{workspace},
		parents:    map[uuid.UUID]uuid.UUID{projectID: workspaceID, stateID: projectID},
	}
	repo := &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"project:identifier:\"CLYDE\"": {
				{
					ID:       projectID,
					OrgID:    orgID,
					NodeType: "project",
					Props:    map[string]json.RawMessage{},
				},
			},
		},
	}
	resolver := &Resolver{
		nodes:             repo,
		reader:            reader,
		members:           &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace",
		entryPointSlug:    "workspace",
		typeIndex: map[string]*node.NodeType{
			"project": {
				TypeKey:      "project",
				Slug:         "project",
				CanLiveUnder: []string{"workspace"},
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceDirectProperty,
					Property: "identifier",
				},
			},
		},
	}
	stateType := &node.NodeType{
		TypeKey:      "state",
		Slug:         "state",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedProperty,
			Property: "name",
		},
	}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))
	id, err := resolver.ResolveTypedNodeID(ctx, stateType, "CLYDE::In Progress")
	if err != nil {
		t.Fatalf("ResolveTypedNodeID(CLYDE::In Progress): %v", err)
	}

	if id != stateID {
		t.Fatalf("ResolveTypedNodeID(CLYDE::In Progress): got %s want %s", id, stateID)
	}

	if audit.ScopeFromContext(ctx).OrgID != orgID {
		t.Fatalf(
			"ResolveTypedNodeID(CLYDE::In Progress) audit org: got %s want %s",
			audit.ScopeFromContext(ctx).OrgID,
			orgID,
		)
	}

	_, err = resolver.ResolveTypedNodeID(ctx, stateType, "In Progress")
	if err == nil || !strings.Contains(err.Error(), domain.ErrInvalidArgument.Error()) {
		t.Fatalf("ResolveTypedNodeID(In Progress): got %v want invalid argument", err)
	}

	resolver.reader = stateListTimeout{reader}
	_, err = resolver.ResolveTypedNodeID(ctx, stateType, "CLYDE::In Progress")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolve state after list timeout: got %v, want deadline exceeded", err)
	}
}

type stateListTimeout struct {
	*resolverReader
}

func (r stateListTimeout) List(ctx context.Context, q node.NodeListQuery) ([]*node.NodeView, error) {
	if q.NodeType == "state" && q.ByTargetRelation != nil {
		return nil, context.DeadlineExceeded
	}

	return r.resolverReader.List(ctx, q)
}
