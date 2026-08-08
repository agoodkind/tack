package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
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

	workspaceType := &node.NodeType{TypeKey: "workspace", Slug: "workspace", Name: "Workspace", Features: node.Features{node.FeatureIsEntryPoint, node.FeatureIsScope}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"}}
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", Name: "Project", CanLiveUnder: []string{"workspace"}, Features: node.Features{node.FeatureIsScope}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", Name: "Issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}, ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()}}
	epicType := &node.NodeType{TypeKey: "epic", Slug: "epic", Name: "Epic", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}, ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()}}
	commentType := &node.NodeType{TypeKey: "comment", Slug: "comment", PluralSlug: "comments", Name: "Comment", CanLiveUnder: []string{"issue", "epic"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly}}

	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Queue", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "Q"), "parent_id": mustRaw(t, workspaceID.String())}}
	issue := &node.NodeView{ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue one", Props: map[string]json.RawMessage{"sequence": mustRaw(t, 1), "scope_id": mustRaw(t, projectID.String()), "parent_id": mustRaw(t, projectID.String())}}
	epic := &node.NodeView{ID: epicID, OrgID: orgID, NodeType: "epic", Name: "Epic two", Props: map[string]json.RawMessage{"sequence": mustRaw(t, 2), "scope_id": mustRaw(t, projectID.String()), "parent_id": mustRaw(t, projectID.String())}}
	issueComment := &node.NodeView{ID: issueCommentID, OrgID: orgID, NodeType: "comment", Name: "On the issue", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, issueID.String())}}
	epicComment := &node.NodeView{ID: epicCommentID, OrgID: orgID, NodeType: "comment", Name: "On the epic", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, epicID.String())}}

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
	}
	repo := &sequenceNodeRepo{
		fakeNodeRepo: &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
			"workspace:slug:\"main\"":  {{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}},
			"project:identifier:\"Q\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
		}},
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

func resultText(t *testing.T, result *mcpmcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	text, ok := result.Content[0].(mcpmcp.TextContent)
	if !ok {
		t.Fatalf("result content = %T, want TextContent", result.Content[0])
	}
	return text.Text
}

// TestScopeRouteSingleParentUnchanged pins that a type with one declared
// parent keeps the exact chain the tools have always required.
func TestScopeRouteSingleParentUnchanged(t *testing.T) {
	fx := newMultiParentFixture(t)
	route := fx.binding.Resolver.ScopeRouteForType(fx.issueType)
	if len(route.Leaves) != 0 {
		t.Fatalf("issue route leaves = %d, want 0", len(route.Leaves))
	}
	want := fx.binding.Resolver.ScopeChainForType(fx.issueType)
	if len(route.Chain) != len(want) {
		t.Fatalf("issue route chain = %v, want %v", route.Chain, want)
	}
	for i := range want {
		if route.Chain[i] != want[i] {
			t.Fatalf("issue route chain[%d] = %v, want %v", i, route.Chain[i], want[i])
		}
	}
}

// TestCreateToolExposesEveryDeclaredParent pins the generated schema for a
// multi-parent type: one optional reference parameter per declared parent,
// with the shared upper chain still required and no leaf forced.
func TestCreateToolExposesEveryDeclaredParent(t *testing.T) {
	fx := newMultiParentFixture(t)
	route := fx.binding.Resolver.ScopeRouteForType(fx.commentType)
	tool := createTool(fx.commentType, "comment", route, "workspace_reference", fx.binding.Resolver)

	for _, param := range []string{"issue_reference", "epic_reference", "project_reference"} {
		if _, ok := tool.InputSchema.Properties[param]; !ok {
			t.Fatalf("create schema missing %s", param)
		}
	}
	required := map[string]bool{}
	for _, name := range tool.InputSchema.Required {
		required[name] = true
	}
	if !required["project_reference"] {
		t.Fatal("project_reference should stay required: every parent lives under the project")
	}
	if required["issue_reference"] || required["epic_reference"] {
		t.Fatalf("leaf params must not be required, got %v", tool.InputSchema.Required)
	}
	if !strings.Contains(tool.Description, "exactly one of issue_reference, epic_reference") {
		t.Fatalf("create description does not name the alternatives: %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "project_reference") {
		t.Fatalf("create description omits the required shared scope parameter: %s", tool.Description)
	}
}

// TestListCommentsUnderSecondParent drives the generated list handler through
// the second declared parent and asserts the scope resolves to that parent:
// only its comments come back.
func TestListCommentsUnderSecondParent(t *testing.T) {
	fx := newMultiParentFixture(t)
	route := fx.binding.Resolver.ScopeRouteForType(fx.commentType)
	handler := listHandler(fx.commentType, route, fx.binding)

	result, err := handler(fx.ctx, callToolReq("tack_list_comments", map[string]any{
		"workspace_reference": "main",
		"project_reference":   "Q",
		"epic_reference":      "Q-2",
	}))
	if err != nil {
		t.Fatalf("listHandler: %v", err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "On the epic") {
		t.Fatalf("epic-parented comment missing from list:\n%s", text)
	}
	if strings.Contains(text, "On the issue") {
		t.Fatalf("issue-parented comment leaked into the epic list:\n%s", text)
	}
}

// TestListCommentsUnderFirstParent proves the first parent keeps working
// through the same route.
func TestListCommentsUnderFirstParent(t *testing.T) {
	fx := newMultiParentFixture(t)
	route := fx.binding.Resolver.ScopeRouteForType(fx.commentType)
	handler := listHandler(fx.commentType, route, fx.binding)

	result, err := handler(fx.ctx, callToolReq("tack_list_comments", map[string]any{
		"workspace_reference": "main",
		"project_reference":   "Q",
		"issue_reference":     "Q-1",
	}))
	if err != nil {
		t.Fatalf("listHandler: %v", err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "On the issue") {
		t.Fatalf("issue-parented comment missing from list:\n%s", text)
	}
	if strings.Contains(text, "On the epic") {
		t.Fatalf("epic-parented comment leaked into the issue list:\n%s", text)
	}
}

// TestRouteRequiresExactlyOneParent pins the error contract when the caller
// provides no leaf reference or more than one.
func TestRouteRequiresExactlyOneParent(t *testing.T) {
	fx := newMultiParentFixture(t)
	route := fx.binding.Resolver.ScopeRouteForType(fx.commentType)
	handler := listHandler(fx.commentType, route, fx.binding)

	for name, args := range map[string]map[string]any{
		"neither": {
			"workspace_reference": "main",
			"project_reference":   "Q",
		},
		"both": {
			"workspace_reference": "main",
			"project_reference":   "Q",
			"issue_reference":     "Q-1",
			"epic_reference":      "Q-2",
		},
	} {
		result, err := handler(fx.ctx, callToolReq("tack_list_comments", args))
		if err != nil {
			t.Fatalf("%s: listHandler: %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s: expected an error result", name)
		}
		text := resultText(t, result)
		if !strings.Contains(text, "exactly one of issue_reference, epic_reference is required") {
			t.Fatalf("%s: error does not state the contract:\n%s", name, text)
		}
	}
}
