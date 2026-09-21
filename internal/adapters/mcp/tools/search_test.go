package tools

import (
	"encoding/json"
	"testing"
)

const temporarySearchOutage = "Search is temporarily unavailable."

func TestSearchReturnsTemporaryOutageBeforeReadingArguments(t *testing.T) {
	fixture := newAuthzFixture(t)
	cases := []struct {
		name      string
		arguments map[string]any
	}{
		{name: "ordinary query", arguments: map[string]any{
			"workspace_reference": "main", "query": "archive",
		}},
		{name: "exact node reference", arguments: map[string]any{
			"workspace_reference": "main", "query": fixture.projectA.String(),
		}},
		{name: "filters", arguments: map[string]any{
			"workspace_reference": "main", "query": "archive", "node_type": "project",
		}},
		{name: "invalid reference", arguments: map[string]any{
			"workspace_reference": "missing", "query": "archive",
		}},
		{name: "omitted arguments", arguments: map[string]any{}},
		{name: "unknown argument", arguments: map[string]any{"unexpected": "value"}},
		{name: "wrong value type", arguments: map[string]any{"query": 42}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := callSearchTool(t, fixture, testCase.arguments)
			if !result.IsError {
				t.Fatal("tack_search returned a successful result")
			}
			if len(result.Content) != 1 {
				t.Fatalf("content length = %d, want 1", len(result.Content))
			}
			content := result.Content[0]
			if content.Type != "text" {
				t.Fatalf("content type = %q, want text", content.Type)
			}
			if content.Text != temporarySearchOutage {
				t.Fatalf("text = %q, want %q", content.Text, temporarySearchOutage)
			}
		})
	}
}

type searchToolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func callSearchTool(t *testing.T, fixture *authzFixture, arguments map[string]any) searchToolResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "tack_search",
			"arguments": arguments,
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw := fixture.server.HandleMessage(fixture.ctx(), body)
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var response struct {
		Result searchToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, encoded)
	}
	if response.Error != nil {
		t.Fatalf("protocol error: %s", response.Error.Message)
	}
	return response.Result
}
