package integration

import "testing"

const unavailableSearchMessage = "Search is temporarily unavailable."

func TestSearchAlwaysReportsTemporaryOutage(t *testing.T) {
	harness := NewMCPHarness(t)
	issueReference := harness.CreateIssue(t, "Temporal WAL archive rollout")

	cases := []struct {
		name      string
		arguments map[string]any
	}{
		{name: "ordinary query", arguments: map[string]any{
			"workspace_reference": harness.Workspace, "query": "archive",
		}},
		{name: "exact title", arguments: map[string]any{
			"workspace_reference": harness.Workspace, "query": "Temporal WAL archive rollout",
		}},
		{name: "exact node reference", arguments: map[string]any{
			"workspace_reference": harness.Workspace, "query": issueReference,
		}},
		{name: "filters", arguments: map[string]any{
			"workspace_reference": harness.Workspace, "query": "archive", "node_type": "issue",
		}},
		{name: "invalid reference", arguments: map[string]any{
			"workspace_reference": "missing-workspace", "query": "archive",
		}},
		{name: "omitted arguments", arguments: map[string]any{}},
		{name: "unknown argument", arguments: map[string]any{"unexpected": "value"}},
		{name: "wrong value type", arguments: map[string]any{"query": 42}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := callToolRaw(t, harness, "tack_search", testCase.arguments)
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
			if content.Text != unavailableSearchMessage {
				t.Fatalf("text = %q, want %q", content.Text, unavailableSearchMessage)
			}
		})
	}
}
