package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const changedValuesMaxBytes = 1024

func changedValueArguments(harness *integration.MCPHarness, project string) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: project, IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

func changedValueText(value string) json.RawMessage {
	return json.RawMessage(strconv.Quote(value))
}

func updateIssueProperties(t *testing.T, harness *integration.MCPHarness, issue string, properties datagen.NodeProperties) string {
	t.Helper()
	arguments := changedValueArguments(harness, "")
	arguments.NodeID = issue
	arguments.Properties = properties
	return harness.Call(t, "tack_update_issue", arguments).Text()
}

func requireConfirmationLines(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, line := range want {
		if !strings.Contains(text, "\n"+line+"\n") {
			t.Fatalf("confirmation lacks the line %q:\n%s", line, text)
		}
	}
}

// TestChangedIssueValues checks old and new reference and scalar values, removal,
// repeated updates, and the state setter through the public tools.
// The harness uses the test name as the project identifier. This short name
// lets the state reference print in full under the 40-byte value limit.
func TestChangedIssueValues(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	issue := harness.CreateIssue(t, "readable changed values")
	todo := harness.Project + "::Todo"
	inProgress := harness.Project + "::In Progress"
	done := harness.Project + "::Done"
	request := datagen.NodeProperties{
		"state_id": changedValueText(inProgress), "priority": changedValueText("high"),
		"description": changedValueText("first description"),
	}

	first := updateIssueProperties(t, harness, issue, request)

	requireConfirmationLines(t, first,
		"- Set: `description` to `first description`",
		"- Changed: `priority` from `none` to `high`",
		"- Changed: `state_id` from `"+todo+"` to `"+inProgress+"`",
	)
	repeated := updateIssueProperties(t, harness, issue, request)
	requireConfirmationLines(t, repeated,
		"- Unchanged: `description` is `first description`",
		"- Unchanged: `priority` is `high`",
		"- Unchanged: `state_id` is `"+inProgress+"`",
	)
	removed := updateIssueProperties(t, harness, issue, datagen.NodeProperties{"description": json.RawMessage("null")})
	requireConfirmationLines(t, removed, "- Removed: `description` (was `first description`)")
	setter := harness.SetIssueState(t, issue, done)
	requireConfirmationLines(t, setter, "- Changed: `state_id` from `"+inProgress+"` to `"+done+"`")
}

// TestChangedEpicValues checks that creating an epic prints the requested values.
// The harness uses the test name as the project identifier. This short name
// lets the state reference print in full under the 40-byte value limit.
func TestChangedEpicValues(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	arguments := changedValueArguments(harness, harness.Project)
	arguments.Name = "readable epic values"
	arguments.Properties = datagen.NodeProperties{
		"priority": changedValueText("urgent"), "state_id": changedValueText(harness.Project + "::Done"),
	}

	text := harness.Call(t, "tack_create_epic", arguments).Text()

	requireConfirmationLines(t, text,
		"- Set: `priority` to `urgent`",
		"- Set: `state_id` to `"+harness.Project+"::Done`",
	)
}

// TestChangedValuesLimit changes five long fields twice. The second update
// prints the longest lines the confirmation allows. The test checks the
// 1024-byte limit and the count of fields left out.
func TestChangedValuesLimit(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	issue := harness.CreateIssue(t, "many long changed values")
	update := func(suffix string, draft string, state string) string {
		arguments := changedValueArguments(harness, "")
		arguments.NodeID = issue
		arguments.Name = "renamed " + strings.Repeat("n", 5*1024) + suffix
		arguments.Properties = datagen.NodeProperties{
			"description": changedValueText(strings.Repeat("A long description line.\n", 400) + suffix),
			"priority":    changedValueText("urgent"),
			"is_draft":    json.RawMessage(draft),
			"state_id":    changedValueText(harness.Project + "::" + state),
		}
		return harness.Call(t, "tack_update_issue", arguments).Text()
	}
	update("first", "true", "Done")

	text := update("second", "false", "Todo")

	if len(text) > changedValuesMaxBytes {
		t.Fatalf("confirmation is %d bytes, want at most %d:\n%s", len(text), changedValuesMaxBytes, text)
	}
	if !strings.Contains(text, "\n- More: the request covered 1 more field, and `tack_get_issue` returns the full node\n") {
		t.Fatalf("confirmation lacks the hidden field count:\n%s", text)
	}
}
