package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func createParentIntegrityNode(
	t *testing.T,
	harness *integration.MCPHarness,
	tool string,
	name string,
	parentID string,
) string {
	t.Helper()
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.Name = name
	if parentID != "" {
		arguments.ProjectReference = ""
		parent := storedParentView(t, parentIntegrityStores(t), parentID)
		switch parent.NodeType {
		case "epic":
			arguments.EpicReference = parentID
		case "issue":
			arguments.IssueReference = parentID
		default:
			t.Fatalf(
				"create fixture under parent %s of unsupported type %s",
				parentID,
				parent.NodeType,
			)
		}
	}

	return harness.Call(t, tool, arguments).RawID()
}

// parentIntegrityArguments returns MCP tool arguments for the harness workspace
// and the project reference project. The function leaves every other field empty.
func parentIntegrityArguments(
	harness *integration.MCPHarness,
	project string,
) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace,
		ProjectReference:   project,
		IssueReference:     "",
		Name:               "",
		Properties:         nil,
		NodeID:             "",
		Query:              "",
		NodeType:           "",
		Direction:          "",
		SourceID:           "",
		RelationType:       "",
		TargetID:           "",
		Limit:              0,
		Cursor:             "",
	}
}

// parentIntegrityStores opens FoundationDB stores on the test cluster that the
// MCP harness uses.
func parentIntegrityStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(
		testenv.FoundationDB(t),
		parentIntegrityTransactionTimeout,
		nil,
	)
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

// requireParents checks the stored parent and all child_of targets.
func requireParents(
	t *testing.T,
	stores *fdbadapter.Stores,
	nodeID string,
	targets []string,
) {
	t.Helper()
	view := storedParentView(t, stores, nodeID)
	primary, err := stores.Nodes.Get(t.Context(), view.OrgID, view.ID)
	if err != nil || primary == nil {
		t.Fatalf("read primary node %s: %v", nodeID, err)
	}

	for _, props := range []map[string]json.RawMessage{primary.Props, view.Props} {
		var parentID string
		if err := json.Unmarshal(props["parent_id"], &parentID); err != nil ||
			parentID != targets[0] {
			t.Errorf("node %s stores parent %q, want %s: %v", nodeID, parentID, targets[0], err)
		}
	}

	edges, err := stores.Relationships.ListBySource(
		t.Context(),
		view.OrgID,
		view.ID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("child_of lookup for node %s failed: %v", nodeID, err)
	}

	stored := make(map[string]bool, len(edges))
	for _, edge := range edges {
		stored[edge.TargetID.String()] = true
	}

	if len(edges) != len(targets) {
		t.Errorf(
			"node %s has %d child_of edges; the test expects %d to %v",
			nodeID,
			len(edges),
			len(targets),
			targets,
		)
	}

	for _, target := range targets {
		if !stored[target] {
			t.Errorf(
				"node %s does not have a child_of edge to %s; stored targets are %v",
				nodeID,
				target,
				stored,
			)
		}
	}
}

func storedParentTarget(t *testing.T, stores *fdbadapter.Stores, view *node.NodeView) string {
	t.Helper()
	edges, err := stores.Relationships.ListBySource(
		t.Context(),
		view.OrgID,
		view.ID,
		node.RelChildOf,
	)
	if err != nil || len(edges) != 1 {
		t.Fatalf("parent edges of node %s = %v, %v, want one", view.ID, edges, err)
	}

	return edges[0].TargetID.String()
}
