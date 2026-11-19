package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
)

func (f *authzFixture) ctx() context.Context {
	return WithAuditRecorder(auth.WithUser(context.Background(), uuid.New()), f.recorder)
}

// callTool drives the registered tool through the real JSON-RPC surface, so
// the strict-args and fail-closed wrappers run exactly as they do in the app.
func (f *authzFixture) callTool(t *testing.T, name string, args map[string]string) (bool, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
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
