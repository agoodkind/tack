package tools

import (
	"context"
	"strings"
	"testing"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
)

// TestUUIDPathServesOwnOrg is the control: the same calls succeed on a node
// of the caller's own org, so the refusals above are the membership check and
// not a broken fixture.
func TestUUIDPathServesOwnOrg(t *testing.T) {
	f := newAuthzFixture(t)
	for _, testCase := range []struct {
		tool string
		args map[string]string
	}{
		{
			tool: "tack_get_project",
			args: map[string]string{"node_id": f.projectA.String(), "workspace_reference": "main"},
		},
		{tool: "tack_get_properties", args: map[string]string{"node_id": f.projectA.String()}},
		{tool: "tack_list_relationships", args: map[string]string{"node_id": f.projectA.String()}},
	} {
		t.Run(
			testCase.tool,
			func(t *testing.T) {
				isError, text := f.callTool(t, testCase.tool, testCase.args)
				if isError {
					t.Fatalf("%s on the caller's own node failed:\n%s", testCase.tool, text)
				}
			},
		)
	}
}

// TestWrapperRefusesUnauthorizedResult pins part 2: a handler that returns
// data without any membership check is refused by the wrapper with permission
// denied, and the refusal lands in the audit ledger. Marking the call
// authorized is the removal check: the same handler then passes.
func TestWrapperRefusesUnauthorizedResult(t *testing.T) {
	recorder := audit.NewMemoryRecorder()

	server := mcpserver.NewMCPServer("tack", "0.2.0")
	registerTool(
		server,
		mcpmcp.Tool{
			Name:        "tack_create_leaks",
			Description: "test tool",
			InputSchema: schema{}.toMCP(),
		},
		func(context.Context, mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			return successText("the crown jewels", ""), nil
		},
	)
	registerTool(
		server,
		mcpmcp.Tool{
			Name:        "tack_create_marked",
			Description: "test tool",
			InputSchema: schema{}.toMCP(),
		},
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			markAuthorized(ctx)
			return successText("authorized data", ""), nil
		},
	)
	f := &authzFixture{server: server, recorder: recorder}

	isError, text := f.callTool(t, "tack_create_leaks", nil)
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

	isError, text = f.callTool(t, "tack_create_marked", nil)
	if isError {
		t.Fatalf("authorized handler was refused:\n%s", text)
	}

	if !strings.Contains(text, "authorized data") {
		t.Fatalf("authorized handler's data missing:\n%s", text)
	}
}
