package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

// relationshipToolArguments returns tool arguments for one relationship tool call.
func relationshipToolArguments(sourceID, relationType, targetID string) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: "", ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: sourceID, RelationType: relationType,
		TargetID: targetID, Limit: 0, Cursor: "",
	}
}

// outgoingRelationships lists the edges of relationType from nodeID.
func (h *MCPHarness) outgoingRelationships(t *testing.T, nodeID, relationType string) string {
	t.Helper()
	arguments := relationshipToolArguments("", relationType, "")
	arguments.NodeID = nodeID
	arguments.Direction = "out"
	return h.Call(t, "tack_list_relationships", arguments).Text()
}

// rawID reads one node of the harness's workspace through toolName and
// returns the raw id printed in the result.
func (h *MCPHarness) rawID(t *testing.T, toolName, reference string) string {
	t.Helper()
	arguments := h.projectArgs()
	arguments.ProjectReference = ""
	arguments.NodeID = reference
	identifier := h.Call(t, toolName, arguments).RawID()
	if identifier == "" {
		t.Fatalf("%s %s printed no raw id", toolName, reference)
	}
	return identifier
}

// TestRemoveRelationshipRefusesOnlyParent pins TACK-546. The
// tack_remove_relationship tool must refuse to remove an issue's only
// child_of edge, the error must contain the issue and project ids, and the
// edge must remain. A blocked_by edge from the same issue must still be
// removable.
func TestRemoveRelationshipRefusesOnlyParent(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "issue with one parent")
	blocker := harness.CreateIssue(t, "blocking issue")
	issueID := harness.rawID(t, "tack_get_issue", issue)
	projectID := harness.rawID(t, "tack_get_project", harness.Project)

	refusal := harness.CallExpectError(t, "tack_remove_relationship", relationshipToolArguments(issue, "child_of", projectID))

	if !strings.Contains(refusal, issueID) || !strings.Contains(refusal, projectID) {
		t.Fatalf("refusal does not contain issue %s and project %s:\n%s", issueID, projectID, refusal)
	}
	parents := harness.outgoingRelationships(t, issue, "child_of")
	if !strings.Contains(parents, "- Target: `"+harness.Project+"`") {
		t.Fatalf("issue %s lost its child_of edge to %s after a refused removal:\n%s", issue, harness.Project, parents)
	}

	blockerTarget := "- Target: `" + blocker + "`"
	harness.Call(t, "tack_add_relationship", relationshipToolArguments(issue, "blocked_by", blocker))
	if blockers := harness.outgoingRelationships(t, issue, "blocked_by"); !strings.Contains(blockers, blockerTarget) {
		t.Fatalf("issue %s has no blocked_by edge to %s after adding it:\n%s", issue, blocker, blockers)
	}
	harness.Call(t, "tack_remove_relationship", relationshipToolArguments(issue, "blocked_by", blocker))
	blockers := harness.outgoingRelationships(t, issue, "blocked_by")
	if strings.Contains(blockers, blockerTarget) {
		t.Fatalf("issue %s still has a blocked_by edge to %s after its removal:\n%s", issue, blocker, blockers)
	}
}
