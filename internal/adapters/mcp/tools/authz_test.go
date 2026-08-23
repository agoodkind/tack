package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

// authzFixture is one caller in org A facing a workspace and project of org A
// and a foreign project of org B.
type authzFixture struct {
	orgA, orgB         uuid.UUID
	workspaceID        uuid.UUID
	projectA, projectB uuid.UUID
	resolver           *Resolver
	reader             *resolverReader
	server             *mcpserver.MCPServer
	relationships      *fakeRelationships
	projectType        *node.NodeType
}

type fakeRelationships struct{}

func (fakeRelationships) Add(context.Context, *node.Relationship) error { panic("Add called") }

func (fakeRelationships) Remove(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) error {
	panic("Remove called")
}

func (fakeRelationships) ListBySource(context.Context, uuid.UUID, uuid.UUID, string) ([]*node.Relationship, error) {
	return nil, nil
}

func (fakeRelationships) ListByTarget(context.Context, uuid.UUID, uuid.UUID, string) ([]*node.Relationship, error) {
	return nil, nil
}

func newAuthzFixture(t *testing.T) *authzFixture {
	t.Helper()
	f := &authzFixture{
		orgA: uuid.New(), orgB: uuid.New(),
		workspaceID: uuid.New(), projectA: uuid.New(), projectB: uuid.New(),
		resolver: nil, reader: nil, server: nil, relationships: &fakeRelationships{}, projectType: nil,
	}
	workspace := &node.NodeView{ID: f.workspaceID, OrgID: f.orgA, NodeType: "workspace", Name: "Main", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	projectA := &node.NodeView{ID: f.projectA, OrgID: f.orgA, NodeType: "project", Name: "Ours", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "OURS"), "parent_id": mustRaw(t, f.workspaceID.String())}}
	projectB := &node.NodeView{ID: f.projectB, OrgID: f.orgB, NodeType: "project", Name: "Theirs", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "THEIRS")}}
	f.reader = &resolverReader{
		views:      map[uuid.UUID]*node.NodeView{f.workspaceID: workspace, f.projectA: projectA, f.projectB: projectB},
		workspaces: []*node.NodeView{workspace},
	}
	f.projectType = &node.NodeType{
		TypeKey: "project", Slug: "project", Name: "Project",
		CanLiveUnder: []string{"workspace"},
		Reference:    node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
		AllowedOps:   []node.Op{node.OpRead},
	}
	workspaceType := &node.NodeType{TypeKey: "workspace", Slug: "workspace", Features: node.Features{node.FeatureIsEntryPoint}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"workspace:slug:\"main\"": {{ID: f.workspaceID, OrgID: f.orgA, NodeType: "workspace", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}},
	}}
	f.resolver = NewResolver(repo, f.reader, &fakeMembers{orgIDs: []uuid.UUID{f.orgA}}, []*node.NodeType{workspaceType, f.projectType})
	f.server = mcpserver.NewMCPServer("tack", "0.2.0")
	RegisterProperty(f.server, &fakePropertyDefs{defs: nil}, f.resolver)
	RegisterRelationship(f.server, nil, f.relationships, f.resolver)
	RegisterSearch(f.server, searchadapter.Noop{}, f.resolver)
	RegisterNodeTools(f.server, f.projectType, NodeTypeBinding{
		NodeSvc: nil, Reader: f.reader, PropertyDefs: &fakePropertyDefs{defs: nil}, Resolver: f.resolver, Users: nil,
	})
	return f
}

func (f *authzFixture) ctx() context.Context {
	return auth.WithUser(context.Background(), uuid.New())
}

// callTool drives the registered tool through the real JSON-RPC surface, so
// the strict-args and fail-closed wrappers run exactly as they do in the app.
func (f *authzFixture) callTool(t *testing.T, name string, args map[string]string) (bool, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw := f.server.HandleMessage(f.ctx(), body)
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var decoded struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode response: %v\n%s", err, encoded)
	}
	if decoded.Error != nil {
		t.Fatalf("tool %s returned protocol error: %s", name, decoded.Error.Message)
	}
	text := ""
	if len(decoded.Result.Content) > 0 {
		text = decoded.Result.Content[0].Text
	}
	return decoded.Result.IsError, text
}

// TestUUIDPathRefusesForeignOrg pins part 1 of the membership gate: every
// tool family that accepts a raw UUID refuses one from an org the caller is
// not a member of, and serves the caller's own org. Reverting the membership
// check in resolveMemberNodeID fails this test.
func TestUUIDPathRefusesForeignOrg(t *testing.T) {
	f := newAuthzFixture(t)
	cases := []struct {
		tool string
		args func(id uuid.UUID) map[string]string
	}{
		{tool: "tack_get_project", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"node_id": id.String(), "workspace_reference": "main"}
		}},
		{tool: "tack_get_properties", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"node_id": id.String()}
		}},
		{tool: "tack_list_relationships", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"node_id": id.String()}
		}},
		{tool: "tack_add_relationship", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"source_id": id.String(), "relation_type": "watches", "target_id": uuid.New().String()}
		}},
		{tool: "tack_remove_relationship", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"source_id": id.String(), "relation_type": "watches", "target_id": uuid.New().String()}
		}},
		{tool: "tack_search", args: func(id uuid.UUID) map[string]string {
			return map[string]string{"workspace_reference": "main", "query": id.String()}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.tool, func(t *testing.T) {
			isError, text := f.callTool(t, testCase.tool, testCase.args(f.projectB))
			if testCase.tool == "tack_search" {
				// Search falls back to full-text on a refused exact match; the
				// foreign node must not appear in the result.
				if strings.Contains(text, "Theirs") {
					t.Fatalf("search leaked the foreign node:\n%s", text)
				}
				return
			}
			if !isError || !strings.Contains(text, "not found") {
				t.Fatalf("%s with a foreign UUID returned isError=%v:\n%s", testCase.tool, isError, text)
			}
		})
	}
}

// TestUUIDPathServesOwnOrg is the control: the same calls succeed on a node
// of the caller's own org, so the refusals above are the membership check and
// not a broken fixture.
func TestUUIDPathServesOwnOrg(t *testing.T) {
	f := newAuthzFixture(t)
	for _, testCase := range []struct {
		tool string
		args map[string]string
	}{
		{tool: "tack_get_project", args: map[string]string{"node_id": f.projectA.String(), "workspace_reference": "main"}},
		{tool: "tack_get_properties", args: map[string]string{"node_id": f.projectA.String()}},
		{tool: "tack_list_relationships", args: map[string]string{"node_id": f.projectA.String()}},
	} {
		t.Run(testCase.tool, func(t *testing.T) {
			isError, text := f.callTool(t, testCase.tool, testCase.args)
			if isError {
				t.Fatalf("%s on the caller's own node failed:\n%s", testCase.tool, text)
			}
		})
	}
}

// TestGetRequiresWorkspaceReference pins part 3: the get schema requires the
// entry point reference and the handler refuses a call without one.
func TestGetRequiresWorkspaceReference(t *testing.T) {
	f := newAuthzFixture(t)
	tool := getTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(strings.Join(tool.InputSchema.Required, ","), "workspace_reference") {
		t.Fatalf("get schema required = %v, want workspace_reference", tool.InputSchema.Required)
	}
	updateSchema := updateTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(strings.Join(updateSchema.InputSchema.Required, ","), "workspace_reference") {
		t.Fatalf("update schema required = %v, want workspace_reference", updateSchema.InputSchema.Required)
	}
	deleteSchema := deleteTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(strings.Join(deleteSchema.InputSchema.Required, ","), "workspace_reference") {
		t.Fatalf("delete schema required = %v, want workspace_reference", deleteSchema.InputSchema.Required)
	}
	isError, text := f.callTool(t, "tack_get_project", map[string]string{"node_id": f.projectA.String()})
	if !isError || !strings.Contains(text, "workspace_reference is required") {
		t.Fatalf("get without workspace_reference returned isError=%v:\n%s", isError, text)
	}
}

// TestWrapperRefusesUnauthorizedResult pins part 2: a handler that returns
// data without any membership check is refused by the wrapper with permission
// denied, and the refusal lands in the audit ledger. Marking the call
// authorized is the removal check: the same handler then passes.
func TestWrapperRefusesUnauthorizedResult(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	SetAuditRecorder(recorder)
	t.Cleanup(func() { SetAuditRecorder(audit.NoopRecorder{}) })

	server := mcpserver.NewMCPServer("tack", "0.2.0")
	registerTool(server,
		mcpmcp.Tool{Name: "tack_list_leaks", Description: "test tool", InputSchema: schema{}.toMCP()},
		func(context.Context, mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			return successText("the crown jewels", ""), nil
		},
	)
	registerTool(server,
		mcpmcp.Tool{Name: "tack_list_marked", Description: "test tool", InputSchema: schema{}.toMCP()},
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			markAuthorized(ctx)
			return successText("authorized data", ""), nil
		},
	)
	f := &authzFixture{server: server}

	isError, text := f.callTool(t, "tack_list_leaks", nil)
	if !isError || !strings.Contains(text, "Permission denied") {
		t.Fatalf("unchecked handler returned isError=%v:\n%s", isError, text)
	}
	if strings.Contains(text, "crown jewels") {
		t.Fatalf("refusal leaked the handler's data:\n%s", text)
	}
	refusalRecorded := false
	for _, event := range recorder.Events() {
		if event.Error != nil && event.Error.Code == "permission_denied" {
			refusalRecorded = true
		}
	}
	if !refusalRecorded {
		t.Fatalf("no permission_denied row recorded; events: %+v", recorder.Events())
	}

	isError, text = f.callTool(t, "tack_list_marked", nil)
	if isError {
		t.Fatalf("authorized handler was refused:\n%s", text)
	}
	if !strings.Contains(text, "authorized data") {
		t.Fatalf("authorized handler's data missing:\n%s", text)
	}
}

// TestMembershipComesFromRequestContextWhenAttached pins that the resolvers
// read the per-request membership set: with the context naming only org B,
// the same caller is refused on an org A node even though the members
// repository would allow it.
func TestMembershipComesFromRequestContextWhenAttached(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := auth.WithOrgMembership(auth.WithUser(context.Background(), uuid.New()), []uuid.UUID{f.orgB})
	ctx = withAuthorization(ctx)
	_, err := f.resolver.ResolveNodeID(ctx, f.projectA.String())
	if err == nil {
		t.Fatal("ResolveNodeID served an org outside the request's membership set")
	}
	if isAuthorized(ctx) {
		t.Fatal("refused resolution still marked the call authorized")
	}
}
