package datagen

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestEnsureNodeUsesCreateRawIDForReusedNode(t *testing.T) {
	t.Parallel()
	const rawID = "0196fca8-1c28-7b6e-987a-b08b165dbf30"
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Params.Name != "tack_create_label" {
			t.Errorf("tool name = %q", payload.Params.Name)
		}
		if payload.Params.Arguments.Name != "release-blocker" {
			t.Errorf("name = %q", payload.Params.Arguments.Name)
		}
		if payload.Params.Arguments.WorkspaceReference != "qa-workspace" {
			t.Errorf(
				"workspace reference = %q",
				payload.Params.Arguments.WorkspaceReference,
			)
		}
		writer.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(writer).Encode(rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      "1",
			Result: Result{Content: []ToolContent{{
				Type: "text",
				Text: "- Raw id: `" + rawID + "`",
			}}},
		})
		if err != nil {
			t.Errorf("encode response: %v", err)
		}
	})
	generator := &Generator{driver: NewDriver(testGraph(handler), false, 245)}
	existing := Result{Content: []ToolContent{{
		Type: "text",
		Text: "- `QAPRJ::release-blocker`\n  - Name: release-blocker",
	}}}
	got, created, err := generator.ensureNode(
		t.Context(),
		"token",
		existing,
		"tack_create_label",
		"release-blocker",
		ToolArguments{
			WorkspaceReference: "qa-workspace",
			Name:               "release-blocker",
		},
	)
	if err != nil {
		t.Fatalf("ensureNode() error = %v", err)
	}
	if created {
		t.Fatal("ensureNode() created = true")
	}
	if got != rawID {
		t.Fatalf("ensureNode() raw ID = %q", got)
	}
}
