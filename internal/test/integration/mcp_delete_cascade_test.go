package integration

import (
	"fmt"
	"strings"
	"testing"
)

// defaultProjectStates is the count of states the seed creates under every
// project.
const defaultProjectStates = 5

// TestDeleteProjectToolReportsDeletedNodes deletes a project with one issue
// through tack_delete_project. The output must count the project, its
// default states, and the issue as deleted nodes, and tack_get_issue must
// refuse the issue.
func TestDeleteProjectToolReportsDeletedNodes(t *testing.T) {
	harness := NewMCPHarness(t)
	issueReference := harness.CreateIssue(t, "Cascade issue")

	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.NodeID = harness.Project
	text := harness.Call(t, "tack_delete_project", arguments).Text()
	for _, want := range []string{fmt.Sprintf("- Deleted nodes: %d", 1+defaultProjectStates+1), "- Delete state: finished"} {
		if !strings.Contains(text, want) {
			t.Fatalf("delete output does not contain %q:\n%s", want, text)
		}
	}

	lookup := harness.projectArgs()
	lookup.ProjectReference = ""
	lookup.NodeID = issueReference
	message := harness.CallExpectError(t, "tack_get_issue", lookup)
	if !strings.Contains(message, "not found") {
		t.Fatalf("get of the deleted issue failed with %q, want not found", message)
	}
}
