package integration_test

import (
	"encoding/json"
	"strconv"
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
// issue to store the expected parent_id and exactly one child_of edge to that
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
		requireParents(t, stores, child, parent, []string{parent})
	}
}

// TestTwoParentIssueEditsWithoutAnotherParent checks MCP updates on an issue
// with parent_id at a project and child_of edges to that project and an epic.
// The test requires a rename to preserve that state. The test then requires an
// explicit parent update to set parent_id to the epic and leave exactly one
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
	projectID := stringProperty(t, stored, "parent_id")
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
	requireParents(t, stores, issue, projectID, []string{projectID, epic})

	move := parentIntegrityArguments(harness, "")
	move.NodeID = issue
	move.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(epic))}
	harness.Call(t, "tack_update_issue", move)
	requireParents(t, stores, issue, epic, []string{epic})
}
