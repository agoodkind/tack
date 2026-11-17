package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// parentIntegrityTransactionTimeout limits transactions to 10 seconds in the
// FoundationDB stores that the MCP parent tests open to read stored records.
const parentIntegrityTransactionTimeout = 10 * time.Second

// TestCreateIssueStoresOneParentEdge creates an epic, an issue under that epic,
// and a nested issue under that issue through MCP tools. The test requires each
// issue to store exactly one child_of edge to that
// parent.
func TestCreateIssueStoresOneParentEdge(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	epic := createParentIntegrityNode(t, harness, "tack_create_epic", "Parent epic", "")
	issue := createParentIntegrityNode(
		t,
		harness,
		"tack_create_issue",
		"Issue under the epic",
		epic,
	)
	nested := createParentIntegrityNode(
		t,
		harness,
		"tack_create_issue",
		"Issue under the issue",
		issue,
	)

	for child, parent := range map[string]string{issue: epic, nested: issue} {
		requireParents(t, stores, child, []string{parent})
	}
}

// TestTwoParentIssueEditsWithoutAnotherParent checks MCP updates on an issue
// with child_of edges to a project and an epic.
// The test requires a rename to preserve that state. The test then requires an
// explicit parent update to leave exactly one
// child_of edge to the epic.
func TestTwoParentIssueEditsWithoutAnotherParent(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	epic := createParentIntegrityNode(t, harness, "tack_create_epic", "Second parent epic", "")
	issue := createParentIntegrityNode(
		t,
		harness,
		"tack_create_issue",
		"Issue with two parents",
		"",
	)
	stored := storedParentView(t, stores, issue)
	projectID := storedParentTarget(t, stores, stored)
	second := &node.Relationship{
		OrgID:        stored.OrgID,
		SourceID:     stored.ID,
		RelationType: node.RelChildOf,
		TargetID:     uuid.MustParse(epic),
		CreatedBy:    uuid.Nil,
		CreatedAt:    time.Now().UTC(),
		Props:        nil,
	}
	if err := stores.Relationships.Add(
		integration.WithTestOperation(t, t.Context()),
		second,
	); err != nil {
		t.Fatalf(
			"relationship store could not add child_of from issue %s to epic %s: %v",
			issue,
			epic,
			err,
		)
	}

	rename := parentIntegrityArguments(harness, "")
	rename.NodeID = issue
	rename.Name = "Renamed issue with two parents"
	harness.Call(t, "tack_update_issue", rename)
	requireParents(t, stores, issue, []string{projectID, epic})

	move := datagen.ToolArguments{SourceID: issue, RelationType: node.RelChildOf, TargetID: epic}
	harness.Call(t, "tack_add_relationship", move)
	requireParents(t, stores, issue, []string{epic})
}
