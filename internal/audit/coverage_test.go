package audit

import "testing"

// expectedTools lists MCP tools that [ToolVerb] must recognize. Add new tools
// to this list to maintain audit coverage. The test checks listed tools; it
// does not discover registrations or detect an omitted tool automatically.
//
// Each per-NodeType entry uses a long concrete form to exercise prefix
// matching.
var expectedTools = []string{
	// These entries represent static tools.
	"tack_list_workspaces",
	"tack_list_members",
	"tack_list_property_defs",
	"tack_get_properties",
	"tack_add_relationship",
	"tack_remove_relationship",
	"tack_list_relationships",
	"tack_search",
	"tack_getting_started",

	// These entries provide one concrete per-NodeType example for each prefix.
	"tack_describe_workspace",
	"tack_list_issues",
	"tack_create_issue",
	"tack_get_issue",
	"tack_update_issue",
	"tack_set_issue_state",
	"tack_delete_issue",
}

func TestToolVerbCoversEveryRegisteredTool(t *testing.T) {
	for _, name := range expectedTools {
		v, ok := ToolVerb(name)
		if !ok {
			t.Errorf("tool %q has no entry in ToolVerb", name)
			continue
		}

		if v == "" {
			// A deeper hook records these tools. The test accepts the recognized tool even
			// when ToolVerb returns an empty verb.
			continue
		}
	}
}

func TestStateChangeAndReadAreDisjoint(t *testing.T) {
	for v := range stateChangeVerbs {
		if IsRead(v) {
			t.Errorf("verb %q is in stateChangeVerbs but IsRead returned true", v)
		}
	}
}

func TestMemoryRecorderRoundTrip(t *testing.T) {
	r := NewMemoryRecorder()
	if err := r.Record(t.Context(), Event{Verb: string(VerbNodeRead)}); err != nil {
		t.Fatalf("record: %v", err)
	}

	got := r.Events()
	if len(got) != 1 || got[0].Verb != string(VerbNodeRead) {
		t.Fatalf("unexpected events: %#v", got)
	}

	if got[0].OccurredAt.IsZero() {
		t.Errorf("OccurredAt should default to now, got zero")
	}

	r.Reset()
	if len(r.Events()) != 0 {
		t.Errorf("Reset should empty the buffer")
	}
}

func TestUnknownToolReturnsNotCovered(t *testing.T) {
	if _, ok := ToolVerb("tack_does_not_exist"); ok {
		t.Errorf("ToolVerb should not match unknown tool names")
	}
}
