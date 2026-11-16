package audit

import "testing"

// expectedTools lists the MCP tools that ToolVerb must recognize. Add new tools
// here to check their audit coverage. Concrete NodeType tool names exercise
// each supported prefix in ToolVerb.
var expectedTools = []string{
	"tack_list_workspaces",
	"tack_list_members",
	"tack_list_property_defs",
	"tack_get_properties",
	"tack_add_relationship",
	"tack_remove_relationship",
	"tack_list_relationships",
	"tack_search",
	"tack_getting_started",

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
			t.Errorf("ToolVerb does not define an entry for tool %q", name)
			continue
		}

		if v == "" {
			// An empty verb indicates that another hook records the operation.
			continue
		}
	}
}

func TestUnknownToolReturnsNotCovered(t *testing.T) {
	if _, ok := ToolVerb("tack_does_not_exist"); ok {
		t.Errorf("ToolVerb should not match unknown tool names")
	}
}
