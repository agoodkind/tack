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

// multiParentFixture mirrors the seed shape that exposes the multi-parent
// route: a comment-like type that declares two allowed parents, both scoped
// sequence types under one project.
type multiParentFixture struct {
	binding        NodeTypeBinding
	commentType    *node.NodeType
	issueType      *node.NodeType
	issueCommentID uuid.UUID
	epicCommentID  uuid.UUID
	epicID         uuid.UUID
	ctx            context.Context
}

func newMultiParentFixture(t *testing.T) *multiParentFixture {
	t.Helper()
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	epicID := uuid.New()
	issueCommentID := uuid.New()
	epicCommentID := uuid.New()

	workspaceType := &node.NodeType{
		TypeKey:   "workspace",
		Slug:      "workspace",
		Name:      "Workspace",
		Features:  node.Features{node.FeatureIsEntryPoint, node.FeatureIsScope},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"},
	}
	projectType := &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		Name:         "Project",
		CanLiveUnder: []string{"workspace"},
		Features:     node.Features{node.FeatureIsScope},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
	}
	issueType := &node.NodeType{
		TypeKey:      "issue",
		Slug:         "issue",
		Name:         "Issue",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
		ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()},
	}
	epicType := &node.NodeType{
		TypeKey:      "epic",
		Slug:         "epic",
		Name:         "Epic",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
		ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()},
	}
	commentType := &node.NodeType{
		TypeKey:      "comment",
		Slug:         "comment",
		PluralSlug:   "comments",
		Name:         "Comment",
		CanLiveUnder: []string{"issue", "epic"},
		Reference:    node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly},
	}

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
		Name:     "Queue",
		Props: map[string]json.RawMessage{
			"identifier": mustRaw(t, "Q"),
			"parent_id":  mustRaw(t, workspaceID.String()),
		},
	}
	issue := &node.NodeView{
		ID:       issueID,
		OrgID:    orgID,
		NodeType: "issue",
		Name:     "Issue one",
		Props: map[string]json.RawMessage{
			"sequence":  mustRaw(t, 1),
			"scope_id":  mustRaw(t, projectID.String()),
			"parent_id": mustRaw(t, projectID.String()),
		},
	}
	epic := &node.NodeView{
		ID:       epicID,
		OrgID:    orgID,
		NodeType: "epic",
		Name:     "Epic two",
		Props: map[string]json.RawMessage{
			"sequence":  mustRaw(t, 2),
			"scope_id":  mustRaw(t, projectID.String()),
			"parent_id": mustRaw(t, projectID.String()),
		},
	}
	issueComment := &node.NodeView{
		ID:       issueCommentID,
		OrgID:    orgID,
		NodeType: "comment",
		Name:     "On the issue",
		Props:    map[string]json.RawMessage{"parent_id": mustRaw(t, issueID.String())},
	}
	epicComment := &node.NodeView{
		ID:       epicCommentID,
		OrgID:    orgID,
		NodeType: "comment",
		Name:     "On the epic",
		Props:    map[string]json.RawMessage{"parent_id": mustRaw(t, epicID.String())},
	}

	reader := &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			workspaceID:    workspace,
			projectID:      project,
			issueID:        issue,
			epicID:         epic,
			issueCommentID: issueComment,
			epicCommentID:  epicComment,
		},
		workspaces: []*node.NodeView{workspace},

		parents: map[uuid.UUID]uuid.UUID{
			projectID:      workspaceID,
			issueID:        projectID,
			epicID:         projectID,
			issueCommentID: issueID,
			epicCommentID:  epicID,
		},
	}
	repo := &sequenceNodeRepo{
		fakeNodeRepo: &fakeNodeRepo{
			scopeChildren: map[string][]*node.Node{
				"workspace:slug:\"main\"": {{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}},
				"project:identifier:\"Q\"": {
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
		holders: map[string]uuid.UUID{
			"reference:Q-1": issueID,
			"reference:Q-2": epicID,
		},
	}
	nodeTypes := []*node.NodeType{workspaceType, projectType, issueType, epicType, commentType}
	resolver := NewResolver(repo, reader, &fakeMembers{orgIDs: []uuid.UUID{orgID}}, nodeTypes)
	binding := NodeTypeBinding{
		NodeSvc:      nil,
		Reader:       reader,
		PropertyDefs: &fakePropertyDefs{},
		Resolver:     resolver,
		Users:        nil,
	}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))

	return &multiParentFixture{
		binding:        binding,
		commentType:    commentType,
		issueType:      issueType,
		issueCommentID: issueCommentID,
		epicCommentID:  epicCommentID,
		epicID:         epicID,
		ctx:            ctx,
	}
}
