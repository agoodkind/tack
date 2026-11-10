package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

// createEpic creates one epic in the harness's project and returns the
// reference printed in the create confirmation.
func (h *MCPHarness) createEpic(t *testing.T, name string) string {
	t.Helper()
	arguments := h.projectArgs()
	arguments.Name = name
	text := h.Call(t, "tack_create_epic", arguments).Text()
	match := confirmationReferencePattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("create epic confirmation has no reference:\n%s", text)
	}
	return match[1]
}

// getIssue reads one issue by reference and returns the rendered text.
func (h *MCPHarness) getIssue(t *testing.T, reference string) string {
	t.Helper()
	arguments := h.projectArgs()
	arguments.ProjectReference = ""
	arguments.NodeID = reference
	return h.Call(t, "tack_get_issue", arguments).Text()
}

func parentProperties(reference string) datagen.NodeProperties {
	return datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(reference))}
}

// TestUpdateIssueReparentsByPrintedReference pins TACK-523: an update whose
// parent_id is a printed epic reference must resolve it to the epic's id and
// move the child_of edge, and the issue must still resolve afterwards.
func TestUpdateIssueReparentsByPrintedReference(t *testing.T) {
	harness := NewMCPHarness(t)
	epic := harness.createEpic(t, "reparent target epic")
	issue := harness.CreateIssue(t, "reparented issue")
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.NodeID = issue
	arguments.Properties = parentProperties(epic)

	harness.Call(t, "tack_update_issue", arguments)

	text := harness.getIssue(t, issue)
	if !strings.Contains(text, "- Parent: `"+epic+"` (`epic`)") {
		t.Fatalf("issue %s does not show the epic as its parent:\n%s", issue, text)
	}
	relationships := harness.Call(t, "tack_list_relationships", datagen.ToolArguments{
		WorkspaceReference: "", ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: issue, Query: "", NodeType: "", Direction: "out", SourceID: "", RelationType: "child_of", TargetID: "",
	}).Text()
	if !strings.Contains(relationships, "- Target: `"+epic+"`") {
		t.Fatalf("issue %s child_of edge does not point at %s:\n%s", issue, epic, relationships)
	}
	if strings.Contains(relationships, "- Target: `"+harness.Project+"`") {
		t.Fatalf("issue %s still has a child_of edge to the project:\n%s", issue, relationships)
	}
}

// TestUpdateIssueRejectsUnresolvableParent pins the other half of TACK-523:
// a parent_id that resolves to nothing is refused and the issue is unchanged.
func TestUpdateIssueRejectsUnresolvableParent(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "issue with a bad parent update")
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.NodeID = issue
	arguments.Properties = parentProperties("NOPE-999")

	text := harness.CallExpectError(t, "tack_update_issue", arguments)

	if !strings.Contains(text, "parent_id") {
		t.Fatalf("error does not name parent_id:\n%s", text)
	}
	after := harness.getIssue(t, issue)
	if !strings.Contains(after, "- Parent: `"+harness.Project+"` (`project`)") {
		t.Fatalf("issue %s parent changed after a refused update:\n%s", issue, after)
	}
}
