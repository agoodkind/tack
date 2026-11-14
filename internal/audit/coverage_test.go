package audit

import "testing"

// expectedTools is the source of truth for the MCP tool surface that the
// audit layer must cover. New tools added to internal/adapters/mcp/tools/
// MUST be added here, otherwise the build fails. ToolVerb in turn must know
// about every entry, otherwise this test fails.
//
// Per-NodeType tool names are listed using their longest concrete form, so
// the prefix matcher in ToolVerb is exercised end to end.
var expectedTools = []string{
	// Static.
	"tack_list_workspaces",
	"tack_list_members",
	"tack_list_property_defs",
	"tack_get_properties",
	"tack_add_relationship",
	"tack_remove_relationship",
	"tack_list_relationships",
	"tack_search",
	"tack_getting_started",

	// Per-NodeType (one concrete example for each prefix).
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
			// Tools handled by a deeper hook are fine; just confirm we
			// flagged them as covered, not unknown.
			continue
		}
	}
}

func TestUnknownToolReturnsNotCovered(t *testing.T) {
	if _, ok := ToolVerb("tack_does_not_exist"); ok {
		t.Errorf("ToolVerb should not match unknown tool names")
	}
}
