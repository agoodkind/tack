package datagen

import (
	"encoding/json"
	"net/http"
	"testing"

	"goodkind.io/tack/internal/runtime"
)

// Every create call carries an explicit idempotency key equal to its request
// identity, and a read call carries none, so a rerun with the same seed reuses
// the node the first run made (TACK-476).
func TestDriverCreateCallsCarryTheRequestIdentityAsIdempotencyKey(t *testing.T) {
	t.Parallel()
	seen := map[string]rpcRequest{}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		seen[payload.Params.Name] = payload
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"ok"}]}}`))
	})
	graph := &runtime.Graph{MCPHandler: handler, AuthMiddleware: func(next http.Handler) http.Handler { return next }}
	driver := NewDriver(graph, false, 245)

	args := ToolArguments{WorkspaceReference: "qa-245-o01-w01", Name: "QA Project 1.1 seed 245"}
	if _, err := driver.Call(t.Context(), "token", "tack_create_project", args); err != nil {
		t.Fatalf("create call: %v", err)
	}
	if _, err := driver.Call(t.Context(), "token", "tack_list_projects", ToolArguments{WorkspaceReference: "qa-245-o01-w01"}); err != nil {
		t.Fatalf("list call: %v", err)
	}

	create := seen["tack_create_project"]
	if create.Params.Arguments.IdempotencyKey == "" || create.Params.Arguments.IdempotencyKey != create.ID {
		t.Fatalf("create idempotency_key = %q, want the request id %q", create.Params.Arguments.IdempotencyKey, create.ID)
	}
	again, err := driver.requestID(t.Context(), "token", "tack_create_project", args)
	if err != nil {
		t.Fatalf("requestID: %v", err)
	}
	if again != create.ID {
		t.Fatalf("request id %q differs from a recomputed one %q; a rerun would not reuse the node", create.ID, again)
	}
	if list := seen["tack_list_projects"]; list.Params.Arguments.IdempotencyKey != "" {
		t.Fatalf("list call carried idempotency_key %q, want none", list.Params.Arguments.IdempotencyKey)
	}
}
