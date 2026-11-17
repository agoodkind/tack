package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

func TestNormalizeUpdatePropsResolvesStateReference(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	issueID := uuid.New()
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
		Name:     "Done",
	}
	issue := &node.NodeView{
		ID:       issueID,
		OrgID:    orgID,
		NodeType: "issue",
		Name:     "Finish",
		Props: map[string]json.RawMessage{
			"scope_id": mustRaw(t, projectID.String()),
		},
	}
	reader := &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			workspaceID: workspace,
			projectID:   project,
			stateID:     state,
			issueID:     issue,
		},
		workspaces: []*node.NodeView{workspace},
		parents: map[uuid.UUID]uuid.UUID{
			projectID: workspaceID,
			stateID:   projectID,
			issueID:   projectID,
		},
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
	projectType := &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		CanLiveUnder: []string{"workspace"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
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
	issueType := &node.NodeType{
		TypeKey:  "issue",
		Slug:     "issue",
		Features: node.Features{node.FeatureHasWorkflowStates},
	}
	resolver := &Resolver{
		nodes:             repo,
		reader:            reader,
		members:           &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace",
		entryPointSlug:    "workspace",
		typeIndex: map[string]*node.NodeType{
			"project": projectType,
			"state":   stateType,
			"issue":   issueType,
		},
	}
	props, _, err := normalizeUpdateProps(
		auth.WithUser(context.Background(), uuid.New()),
		NodeTypeBinding{
			Reader: reader,
			PropertyDefs: &fakePropertyDefs{
				defs: []*node.PropertyDef{
					{
						Name:                   "state_id",
						Type:                   node.PropertyTypeUUID,
						AppliesToFeatures:      []string{node.FeatureHasWorkflowStates},
						ReferenceTargetTypeKey: "state",
					},
				},
			},
			Resolver: resolver,
		},
		issueType,
		issue,
		map[string]json.RawMessage{"state_id": mustRaw(t, "CLYDE::Done")},
	)
	if err != nil {
		t.Fatalf("normalizeUpdateProps(state_id): %v", err)
	}

	got := rawUUID(props, "state_id")
	if got != stateID {
		t.Fatalf("normalizeUpdateProps(state_id): got %s want %s", got, stateID)
	}
}
