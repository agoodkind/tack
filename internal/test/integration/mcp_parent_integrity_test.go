package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
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
	issue := createParentIntegrityNode(t, harness, "tack_create_issue", "Issue under the epic", epic)
	nested := createParentIntegrityNode(t, harness, "tack_create_issue", "Issue under the issue", issue)

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
	issue := createParentIntegrityNode(t, harness, "tack_create_issue", "Issue with two parents", "")
	stored := storedParentView(t, stores, issue)
	projectID := stringProperty(t, stored, "parent_id")
	second := &node.Relationship{
		OrgID: stored.OrgID, SourceID: stored.ID, RelationType: node.RelChildOf, TargetID: uuid.MustParse(epic),
		CreatedBy: uuid.Nil, CreatedAt: time.Now().UTC(), Props: nil,
	}
	if err := stores.Relationships.Add(integration.WithTestOperation(t, t.Context()), second); err != nil {
		t.Fatalf("relationship store could not add child_of from issue %s to epic %s: %v", issue, epic, err)
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

// createParentIntegrityNode calls the MCP create tool tool with name in the
// harness project. The function sets parent_id to parentID when parentID is not
// empty and returns the raw ID that the tool prints.
func createParentIntegrityNode(t *testing.T, harness *integration.MCPHarness, tool, name, parentID string) string {
	t.Helper()
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.Name = name
	if parentID != "" {
		arguments.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(parentID))}
	}
	return harness.Call(t, tool, arguments).RawID()
}

// parentIntegrityArguments returns MCP tool arguments for the harness workspace
// and the project reference project. The function leaves every other field empty.
func parentIntegrityArguments(harness *integration.MCPHarness, project string) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: project, IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

// parentIntegrityStores opens FoundationDB stores on the test cluster that the
// MCP harness uses.
func parentIntegrityStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), parentIntegrityTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("test could not open FoundationDB stores: %v", err)
	}
	return stores
}

// storedParentView reads the stored view of nodeID. The function fails the test
// if the read fails or the node does not exist.
func storedParentView(t *testing.T, stores *fdbadapter.Stores, nodeID string) *node.NodeView {
	t.Helper()
	view, err := stores.Views.Get(t.Context(), uuid.MustParse(nodeID))
	if err != nil || view == nil {
		t.Fatalf("read of node %s failed: %v, %v", nodeID, view, err)
	}
	return view
}

// stringProperty decodes the JSON string property name of view. The function
// fails the test if the value is not a JSON string.
func stringProperty(t *testing.T, view *node.NodeView, name string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(view.Props[name], &value); err != nil {
		t.Fatalf("JSON decoding of %s on node %s failed: %v", name, view.ID, err)
	}
	return value
}

// requireParents requires the stored parent_id of nodeID to equal parentID.
// The function also requires the node's child_of edges to target exactly the
// IDs in targets.
func requireParents(t *testing.T, stores *fdbadapter.Stores, nodeID, parentID string, targets []string) {
	t.Helper()
	view := storedParentView(t, stores, nodeID)
	if got := stringProperty(t, view, "parent_id"); got != parentID {
		t.Errorf("node %s has parent_id %q; the test expects %s", nodeID, got, parentID)
	}
	edges, err := stores.Relationships.ListBySource(t.Context(), view.OrgID, view.ID, node.RelChildOf)
	if err != nil {
		t.Fatalf("child_of lookup for node %s failed: %v", nodeID, err)
	}
	stored := make(map[string]bool, len(edges))
	for _, edge := range edges {
		stored[edge.TargetID.String()] = true
	}
	if len(edges) != len(targets) {
		t.Errorf("node %s has %d child_of edges; the test expects %d to %v", nodeID, len(edges), len(targets), targets)
	}
	for _, target := range targets {
		if !stored[target] {
			t.Errorf("node %s does not have a child_of edge to %s; stored targets are %v", nodeID, target, stored)
		}
	}
}
