package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

func TestResolveScopeAcceptsSequenceReference(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
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
			"parent_id":  mustRaw(t, workspaceID.String()),
		},
	}
	issue := &node.NodeView{
		ID:       issueID,
		OrgID:    orgID,
		NodeType: "issue",
		Name:     "Reference work",
		Props: map[string]json.RawMessage{
			"sequence":  mustRaw(t, 157),
			"scope_id":  mustRaw(t, projectID.String()),
			"parent_id": mustRaw(t, projectID.String()),
		},
	}
	reader := &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			workspaceID: workspace,
			projectID:   project,
			issueID:     issue,
		},
		workspaces: []*node.NodeView{workspace},

		parents: map[uuid.UUID]uuid.UUID{projectID: workspaceID, issueID: projectID},
	}
	// The uniqueness index is the only sequence-resolution path, so the fake
	// serves the reference key the repair would have backfilled.
	repo := &sequenceNodeRepo{
		fakeNodeRepo: &fakeNodeRepo{
			scopeChildren: map[string][]*node.Node{
				"project:identifier:\"CLYDE\"": {
					{
						ID:       projectID,
						OrgID:    orgID,
						NodeType: "project",
						Props: map[string]json.RawMessage{
							"parent_id": mustRaw(t, workspaceID.String()),
						},
					},
				},
			},
		},
		holders: map[string]uuid.UUID{"reference:CLYDE-157": issueID},
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
	issueType := &node.NodeType{
		TypeKey:      "issue",
		Slug:         "issue",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
		ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()},
	}
	resolver := &Resolver{
		nodes:             repo,
		reader:            reader,
		members:           &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace",
		entryPointSlug:    "workspace",
		scopeChain: []ScopeLevel{
			{TypeKey: "project", Slug: "project", ParamName: "project_reference"},
		},
		typeIndex: map[string]*node.NodeType{"project": projectType, "issue": issueType},
	}
	ctx := auth.WithUser(context.Background(), uuid.New())
	resolved, err := resolver.ResolveScope(
		ctx,
		project,
		ScopeLevel{TypeKey: "issue", Slug: "issue", ParamName: "issue_reference"},
		"CLYDE-157",
	)
	if err != nil {
		t.Fatalf("ResolveScope(CLYDE-157): %v", err)
	}

	if resolved.ID != issueID {
		t.Fatalf("ResolveScope(CLYDE-157): got %s want %s", resolved.ID, issueID)
	}
}
