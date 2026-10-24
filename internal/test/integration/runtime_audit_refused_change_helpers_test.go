package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// createNode calls tool with arguments and returns the raw ID of the node
// that the tool created.
func (server *publicServer) createNode(t *testing.T, tool string, arguments map[string]any) uuid.UUID {
	t.Helper()
	response := server.rpc(t, newRequestID(t, "create"), "tools/call", map[string]any{"name": tool, "arguments": arguments})
	return uuid.MustParse(rawID(t, toolText(t, response)))
}

// childOfEdges returns the tack_list_relationships text for the outgoing
// child_of edges of nodeID.
func (server *publicServer) childOfEdges(t *testing.T, nodeID uuid.UUID) string {
	t.Helper()
	response := server.rpc(t, newRequestID(t, "parents"), "tools/call", map[string]any{
		"name": "tack_list_relationships",
		"arguments": map[string]any{
			"node_id": nodeID.String(), "direction": "out", "relation_type": "child_of",
		},
	})
	return toolText(t, response)
}

// withName returns a copy of arguments with name set.
func withName(arguments map[string]any, name string) map[string]any {
	named := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		named[key] = value
	}
	named["name"] = name
	return named
}

// requireToolRefusal requires a tools/call response with an error result
// that contains reason.
func requireToolRefusal(t *testing.T, response publicResponse, reason string) {
	t.Helper()
	var decoded struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed for the tools/call response: %v\n%s", err, response.body)
	}
	if !decoded.Result.IsError || len(decoded.Result.Content) == 0 || !strings.Contains(decoded.Result.Content[0].Text, reason) {
		t.Fatalf("tools/call response = %s, want an error result that contains %q", response.body, reason)
	}
}
