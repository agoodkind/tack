package integration_test

import (
	"regexp"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// memberPattern matches one member in tack_list_members output and captures
// the display name title and the operational user ID field below it.
var memberPattern = regexp.MustCompile("(?m)^- (.+)\n  - Operational user id: `([0-9a-f-]+)`")

// TestIssueAssignmentRendersFromAssignedToRelationships checks assignment
// rendering through public MCP tools against real FoundationDB. It creates
// a synthetic issue, assigns a fixture org member with tack_add_relationship,
// and checks that tack_list_issues and tack_get_issue show the member.
// It removes the assignment with tack_remove_relationship and checks that
// both responses show the issue as unassigned. It also checks that the rest
// of tack_get_issue output stays unchanged apart from the Updated field.
func TestIssueAssignmentRendersFromAssignedToRelationships(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	issue := harness.CreateIssue(t, "Synthetic issue for assignment rendering")
	memberName, memberID := firstMember(t, harness)
	before := getIssueText(t, harness, issue)
	requireLine(t, before, "- Assignees: unassigned")

	harness.Call(t, "tack_add_relationship", assignmentArguments(issue, memberID))
	requireLine(t, listedIssue(t, harness, issue), "  - Assigned: "+memberName)
	requireLine(t, getIssueText(t, harness, issue), "- Assignees: "+memberName)

	harness.Call(t, "tack_remove_relationship", assignmentArguments(issue, memberID))
	requireLine(t, listedIssue(t, harness, issue), "  - Assigned: unassigned")
	after := getIssueText(t, harness, issue)
	if withoutUpdated(after) != withoutUpdated(before) {
		t.Fatalf("issue %s changed after the assignment was added and removed:\nbefore:\n%s\nafter:\n%s", issue, before, after)
	}
}

func firstMember(t *testing.T, harness *integration.MCPHarness) (string, string) {
	t.Helper()
	text := harness.Call(t, "tack_list_members", assignmentToolArguments(harness.Workspace, "")).Text()
	match := memberPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("tack_list_members printed no member:\n%s", text)
	}
	return match[1], match[2]
}

func getIssueText(t *testing.T, harness *integration.MCPHarness, issue string) string {
	t.Helper()
	arguments := assignmentToolArguments(harness.Workspace, "")
	arguments.NodeID = issue
	return harness.Call(t, "tack_get_issue", arguments).Text()
}

// listedIssue returns the issue's title line and the field lines below it
// from tack_list_issues output.
func listedIssue(t *testing.T, harness *integration.MCPHarness, issue string) string {
	t.Helper()
	text := harness.Call(t, "tack_list_issues", assignmentToolArguments(harness.Workspace, harness.Project)).Text()
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if line != "- `"+issue+"`" {
			continue
		}
		end := index + 1
		for end < len(lines) && strings.HasPrefix(lines[end], "  - ") {
			end++
		}
		return strings.Join(lines[index:end], "\n")
	}
	t.Fatalf("tack_list_issues did not list issue %s:\n%s", issue, text)
	return ""
}

func requireLine(t *testing.T, text, line string) {
	t.Helper()
	for _, candidate := range strings.Split(text, "\n") {
		if candidate == line {
			return
		}
	}
	t.Fatalf("output has no line %q:\n%s", line, text)
}

// withoutUpdated removes the Updated field line, which shows the time of
// the last write.
func withoutUpdated(text string) string {
	kept := make([]string, 0, strings.Count(text, "\n")+1)
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "- Updated: ") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func assignmentArguments(issue, memberID string) datagen.ToolArguments {
	arguments := assignmentToolArguments("", "")
	arguments.SourceID = issue
	arguments.RelationType = "assigned_to"
	arguments.TargetID = memberID
	return arguments
}

func assignmentToolArguments(workspace, project string) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: workspace, ProjectReference: project, IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}
