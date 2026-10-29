package integration_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const changedValuesMaxBytes = 1024

var hiddenFieldCountPattern = regexp.MustCompile("\n- More: the request covered ([0-9]+) more fields?, and `tack_get_issue` returns the full node\n")

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

func requireShortenedReferenceLine(t *testing.T, text, prefix, middle, suffix string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, middle) && strings.HasSuffix(line, suffix) {
			return
		}
	}
	t.Fatalf("confirmation lacks a line that starts with %q, contains %q, and ends with %q:\n%s", prefix, middle, suffix, text)
}

// TestChangedIssueValuesWithLongProjectIdentifier checks that references shortened
// from the start keep state names visible. The harness uses the test name as the
// project identifier, which exceeds the 40-byte value limit. The test also checks
// scalar, removal, repeated update, empty string, and state setter lines.
func TestChangedIssueValuesWithLongProjectIdentifier(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	issue := harness.CreateIssue(t, "readable changed values")
	request := datagen.NodeProperties{
		"state_id": changedValueText(harness.Project + "::In Progress"), "priority": changedValueText("high"),
		"description": changedValueText("first description"),
	}

	first := updateIssueProperties(t, harness, issue, request)

	requireConfirmationLines(t, first,
		"- Set: `description` to `first description`",
		"- Changed: `priority` from `none` to `high`",
	)
	requireShortenedReferenceLine(t, first, "- Changed: `state_id` from `…", "::Todo` to `…", "::In Progress`")
	repeated := updateIssueProperties(t, harness, issue, request)
	requireConfirmationLines(t, repeated,
		"- Unchanged: `description` is `first description`",
		"- Unchanged: `priority` is `high`",
	)
	requireShortenedReferenceLine(t, repeated, "- Unchanged: `state_id` is `…", "::In Progress`", "::In Progress`")
	removed := updateIssueProperties(t, harness, issue, datagen.NodeProperties{"description": json.RawMessage("null")})
	requireConfirmationLines(t, removed, "- Removed: `description` (was `first description`)")
	empty := updateIssueProperties(t, harness, issue, datagen.NodeProperties{"description": changedValueText("")})
	requireConfirmationLines(t, empty, "- Set: `description` to `\"\"`")
	setter := harness.SetIssueState(t, issue, harness.Project+"::Done")
	requireShortenedReferenceLine(t, setter, "- Changed: `state_id` from `…", "::In Progress` to `…", "::Done`")
}

// TestChangedEpicValues checks that creating an epic prints the requested values.
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

// TestChangedValuesDropLinesForLongReferences uses a 220-byte project identifier.
// The heading, Reference, and Name lines leave little room for four change lines.
// The test checks the 1024-byte limit and at least two hidden fields; the
// four-line limit alone hides one. It also checks that Raw id prints last.
func TestChangedValuesDropLinesForLongReferences(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	identifier := strings.Repeat("LONGPROJECT", 20)
	projectArguments := changedValueArguments(harness, "")
	projectArguments.Name = "long identifier project"
	projectArguments.Properties = datagen.NodeProperties{"identifier": changedValueText(identifier)}
	harness.Call(t, "tack_create_project", projectArguments)
	issueArguments := changedValueArguments(harness, identifier)
	issueArguments.Name = "long reference issue"
	issueID := harness.Call(t, "tack_create_issue", issueArguments).RawID()
	update := func(suffix string, draft string, state string) string {
		arguments := changedValueArguments(harness, "")
		arguments.NodeID = issueID
		arguments.Name = "renamed " + strings.Repeat("n", 5*1024) + suffix
		arguments.Properties = datagen.NodeProperties{
			"description": changedValueText(strings.Repeat("A long description line.\n", 400) + suffix),
			"priority":    changedValueText("urgent"),
			"is_draft":    json.RawMessage(draft),
			"state_id":    changedValueText(identifier + "::" + state),
		}
		return harness.Call(t, "tack_update_issue", arguments).Text()
	}
	update("first", "true", "Done")

	text := update("second", "false", "In Progress")

	if len(text) > changedValuesMaxBytes {
		t.Fatalf("confirmation is %d bytes, want at most %d:\n%s", len(text), changedValuesMaxBytes, text)
	}
	match := hiddenFieldCountPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("confirmation lacks the hidden field count:\n%s", text)
	}
	hidden, err := strconv.Atoi(match[1])
	if err != nil || hidden < 2 {
		t.Fatalf("hidden field count = %q, want at least 2 (the four-line limit hides one field):\n%s", match[1], text)
	}
	if !strings.HasSuffix(text, "- Raw id: `"+issueID+"`\n") {
		t.Fatalf("confirmation does not end with the raw id:\n%s", text)
	}
}
