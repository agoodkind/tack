package integration_test

import (
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// scopeIssues lists the issues with the scope_id of issue.
func scopeIssues(t *testing.T, stores *fdbadapter.Stores, issue *node.NodeView) []*node.Node {
	t.Helper()
	issues, err := stores.Nodes.ListByProperty(
		t.Context(),
		issue.OrgID,
		"issue",
		"scope_id",
		issue.Props["scope_id"],
	)
	if err != nil {
		t.Fatalf("list issues with the scope of issue %s: %v", issue.ID, err)
	}

	return issues
}

// createReferencedNode calls the create tool with name in the harness project
// and returns the raw ID and the printed reference from the confirmation.
func createReferencedNode(
	t *testing.T,
	harness *integration.MCPHarness,
	tool string,
	name string,
) (uuid.UUID, string) {
	t.Helper()
	arguments := schemaArguments(harness)
	arguments.Name = name
	result := harness.Call(t, tool, arguments)
	match := schemaReferencePattern.FindStringSubmatch(result.Text())
	if match == nil {
		t.Fatalf("%s confirmation does not print a reference:\n%s", tool, result.Text())
	}

	return uuid.MustParse(result.RawID()), match[1]
}

// parentMoveArguments returns tack_update_issue arguments that set parent_id
// of issueID to reference.
func parentMoveArguments(
	harness *integration.MCPHarness,
	issueID uuid.UUID,
	reference string,
) datagen.ToolArguments {
	arguments := schemaArguments(harness)
	arguments.ProjectReference = ""
	arguments.NodeID = issueID.String()
	arguments.Properties = datagen.NodeProperties{"parent_id": quoted(reference)}

	return arguments
}

// storedResolve reads the NodeResolve record of nodeID and fails the test when
// the read fails or the record does not exist.
func storedResolve(t *testing.T, stores *fdbadapter.Stores, nodeID uuid.UUID) node.NodeResolve {
	t.Helper()
	resolved, err := stores.Views.Resolve(t.Context(), nodeID)
	if err != nil || resolved == nil {
		t.Fatalf("resolve node %s: record %v, err %v", nodeID, resolved, err)
	}

	return *resolved
}
