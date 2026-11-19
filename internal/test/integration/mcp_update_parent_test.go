package integration

import (
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

// TestUpdateIssueReparentsByPrintedReference checks that an update resolves
// a printed epic reference to the epic UUID and updates the child_of edge.
// The issue must still resolve after the update (TACK-523).
func TestUpdateIssueReparentsByPrintedReference(t *testing.T) {
	harness := NewMCPHarness(t)
	epic := harness.createEpic(t, "reparent target epic")
	issue := harness.CreateIssue(t, "reparented issue")
	harness.Call(t, "tack_add_relationship", relationshipToolArguments(issue, "child_of", epic))

	text := harness.getIssue(t, issue)
	if !strings.Contains(text, "- Parent: `"+epic+"` (`epic`)") {
		t.Fatalf("issue %s does not show the epic as its parent:\n%s", issue, text)
	}

	relationships := harness.Call(
		t,
		"tack_list_relationships",
		datagen.ToolArguments{
			WorkspaceReference: "",
			ProjectReference:   "",
			IssueReference:     "",
			Name:               "",
			Properties:         nil,
			NodeID:             issue,
			Query:              "",
			NodeType:           "",
			Direction:          "out",
			SourceID:           "",
			RelationType:       "child_of",
			TargetID:           "",
		},
	).Text()
	if !strings.Contains(relationships, "- Target: `"+epic+"`") {
		t.Fatalf("issue %s child_of edge does not point at %s:\n%s", issue, epic, relationships)
	}

	if strings.Contains(relationships, "- Target: `"+harness.Project+"`") {
		t.Fatalf("issue %s still has a child_of edge to the project:\n%s", issue, relationships)
	}
}

// TestUpdateIssueRejectsUnresolvableParent pins the other half of TACK-523:
// an unresolved parent reference fails without changing the issue.
func TestUpdateIssueRejectsUnresolvableParent(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "issue with a bad parent update")
	text := harness.CallExpectError(
		t,
		"tack_add_relationship",
		relationshipToolArguments(issue, "child_of", "NOPE-999"),
	)

	if !strings.Contains(text, "NOPE-999") {
		t.Fatalf("error does not identify the unresolved parent:\n%s", text)
	}

	after := harness.getIssue(t, issue)
	if !strings.Contains(after, "- Parent: `"+harness.Project+"` (`project`)") {
		t.Fatalf("issue %s parent changed after a refused update:\n%s", issue, after)
	}
}
