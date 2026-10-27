package tools_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/auth"
)

// rpcError sends one JSON-RPC request to server and returns the message of
// its protocol error, or an empty string when the response has none.
func rpcError(t *testing.T, server *mcpserver.MCPServer, method string, params map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	ctx := auth.WithUser(t.Context(), uuid.New())
	encoded, err := json.Marshal(server.HandleMessage(ctx, body))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, encoded)
	}
	if response.Error == nil {
		return ""
	}
	return response.Error.Message
}

// TestRequestsWithoutAuditRecorderFail is TACK-583: a state-changing tool
// call and a resource read on a context without an audit recorder fail with
// a protocol error that states the recorder is missing. The tool handler
// does not run, because the relationship store here is nil and a call to it
// would panic.
func TestRequestsWithoutAuditRecorderFail(t *testing.T) {
	server := mcpserver.NewMCPServer("tack", "0.2.0")
	resolver := tools.NewResolver(nil, nil, nil, nil)
	tools.RegisterRelationship(server, nil, nil, resolver)
	tools.RegisterResources(server, nil, resolver, nil, nil)

	toolCall := rpcError(t, server, "tools/call", map[string]any{
		"name": "tack_add_relationship",
		"arguments": map[string]any{
			"source_id": uuid.NewString(), "relation_type": "watches", "target_id": uuid.NewString(),
		},
	})
	resourceRead := rpcError(t, server, "resources/read", map[string]any{"uri": "tack://getting-started"})
	for name, message := range map[string]string{"tool call": toolCall, "resource read": resourceRead} {
		if !strings.Contains(message, "audit recorder") {
			t.Fatalf("%s without an audit recorder returned protocol error %q, want a missing audit recorder error", name, message)
		}
	}
}
