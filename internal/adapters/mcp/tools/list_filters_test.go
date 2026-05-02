package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

func TestNormalizeListFiltersResolvesReferenceAlias(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Clyde", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "CLYDE"), "parent_id": mustRaw(t, workspaceID.String())}}
	state := &node.NodeView{ID: stateID, OrgID: orgID, NodeType: "state", Name: "Done", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, projectID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project, stateID: state}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"CLYDE\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
	}}
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}}
	stateType := &node.NodeType{TypeKey: "state", Slug: "state", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", Features: node.Features{node.FeatureHasWorkflowStates}}
	resolver := &Resolver{
		nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace", entryPointSlug: "workspace",
		typeIndex: map[string]*node.NodeType{
			"project": projectType,
			"state":   stateType,
			"issue":   issueType,
		},
	}
	filters := mustRaw(t, map[string]string{"state": "CLYDE::Done", "priority": "high"})

	matches, err := normalizeListFilters(auth.WithUser(context.Background(), uuid.New()), NodeTypeBinding{
		Reader:       reader,
		PropertyDefs: &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "state_id", Type: node.PropertyTypeUUID, AppliesToFeatures: []string{node.FeatureHasWorkflowStates}, ReferenceTargetTypeKey: "state"}}},
		Resolver:     resolver,
	}, issueType, orgID, projectID, filters)
	if err != nil {
		t.Fatalf("normalizeListFilters: %v", err)
	}
	got := map[string]json.RawMessage{}
	for _, match := range matches {
		got[match.PropName] = match.Value
	}
	if rawUUID(got, "state_id") != stateID {
		t.Fatalf("state filter = %s, want %s", rawUUID(got, "state_id"), stateID)
	}
	if string(got["priority"]) != `"high"` {
		t.Fatalf("priority filter = %s, want %q", got["priority"], `"high"`)
	}
}
