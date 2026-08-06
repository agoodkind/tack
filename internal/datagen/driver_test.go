package datagen

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"goodkind.io/tack/internal/runtime"
)

func TestDriverCallUsesAuthenticatedToolsCallRequest(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			t.Errorf("path = %q, want /mcp", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer qa-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("Mcp-Session-Id") == "" {
			t.Error("Mcp-Session-Id is empty")
		}
		var payload rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.JSONRPC != "2.0" || payload.Method != "tools/call" {
			t.Errorf("request = %#v", payload)
		}
		if payload.Params.Name != "tack_list_workspaces" {
			t.Errorf("tool name = %q", payload.Params.Name)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"ok"}]}}`,
		))
	})
	graph := &runtime.Graph{
		MCPHandler: handler,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return next
		},
	}
	driver := NewDriver(graph, false, 245)
	result, err := driver.Call(
		t.Context(),
		"qa-token",
		"tack_list_workspaces",
		ToolArguments{},
	)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.Text() != "ok" {
		t.Fatalf("Call() text = %q, want ok", result.Text())
	}
}

func TestReadLastSSEData(t *testing.T) {
	t.Parallel()
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{}}\n\n")
	payload, err := readLastSSEData(body)
	if err != nil {
		t.Fatalf("readLastSSEData() error = %v", err)
	}
	if strings.TrimSpace(string(payload)) != `{"jsonrpc":"2.0","result":{}}` {
		t.Fatalf("payload = %q", payload)
	}
}

func TestDriverListToolsUsesAuthenticatedToolsListRequest(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer qa-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		var payload listToolsRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.JSONRPC != "2.0" || payload.Method != "tools/list" {
			t.Errorf("request = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"tools":[` +
				`{"name":"tack_list_workspaces"},{"name":"tack_audit_redact_actor"}]}}`,
		))
	})
	graph := &runtime.Graph{
		MCPHandler: handler,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return next
		},
	}
	names, err := NewDriver(graph, false, 245).ListTools(t.Context(), "qa-token")
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if strings.Join(names, ",") != "tack_list_workspaces,tack_audit_redact_actor" {
		t.Fatalf("ListTools() = %v", names)
	}
}

func TestDriverSessionIDDependsOnlyOnSeed(t *testing.T) {
	t.Parallel()
	first := NewDriver(nil, true, 245)
	second := NewDriver(nil, true, 245)
	if first.sessionID != second.sessionID {
		t.Fatalf("session IDs differ: %q != %q", first.sessionID, second.sessionID)
	}
	if first.sessionID != "qa-datagen-245" {
		t.Fatalf("session ID = %q", first.sessionID)
	}
}
