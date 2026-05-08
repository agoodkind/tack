package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestNormalizeCreatePropsCanonicalizesStateAlias(t *testing.T) {
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
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"}}
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
	props, parentID, err := normalizeCreateProps(auth.WithUser(context.Background(), uuid.New()), NodeTypeBinding{
		Reader:       reader,
		PropertyDefs: &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "state_id", Type: node.PropertyTypeUUID, AppliesToFeatures: []string{node.FeatureHasWorkflowStates}, ReferenceTargetTypeKey: "state"}}},
		Resolver:     resolver,
	}, issueType, orgID, projectID, map[string]json.RawMessage{"state": mustRaw(t, "Done")})
	if err != nil {
		t.Fatalf("normalizeCreateProps(state alias): %v", err)
	}
	if parentID != projectID {
		t.Fatalf("parentID = %s, want %s", parentID, projectID)
	}
	if _, ok := props["state"]; ok {
		t.Fatalf("normalized props should not contain raw state alias: %+v", props)
	}
	got := rawUUID(props, "state_id")
	if got != stateID {
		t.Fatalf("normalizeCreateProps(state alias): got %s want %s", got, stateID)
	}
}

func TestNormalizeCreatePropsRejectsUnknownProperty(t *testing.T) {
	orgID := uuid.New()
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue"}
	resolver := &Resolver{typeIndex: map[string]*node.NodeType{"issue": issueType}}
	_, _, err := normalizeCreateProps(context.Background(), NodeTypeBinding{
		PropertyDefs: &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "priority", Type: node.PropertyTypeText}}},
		Resolver:     resolver,
	}, issueType, orgID, uuid.New(), map[string]json.RawMessage{"made_up_field": mustRaw(t, "x")})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("normalizeCreateProps unknown property err = %v, want ErrInvalidArgument", err)
	}
}

func TestNormalizeCreatePropsRejectsAliasAndCanonicalTogether(t *testing.T) {
	orgID := uuid.New()
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", Features: node.Features{node.FeatureHasWorkflowStates}}
	resolver := &Resolver{typeIndex: map[string]*node.NodeType{"issue": issueType, "state": {TypeKey: "state", Slug: "state", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}}}}
	_, _, err := normalizeCreateProps(context.Background(), NodeTypeBinding{
		PropertyDefs: &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "state_id", Type: node.PropertyTypeUUID, AppliesToFeatures: []string{node.FeatureHasWorkflowStates}, ReferenceTargetTypeKey: "state"}}},
		Resolver:     resolver,
	}, issueType, orgID, uuid.New(), map[string]json.RawMessage{
		"state":    mustRaw(t, "Done"),
		"state_id": mustRaw(t, "Done"),
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("normalizeCreateProps ambiguous err = %v, want ErrInvalidArgument", err)
	}
}
