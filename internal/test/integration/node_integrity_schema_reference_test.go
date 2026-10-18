package integration_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const schemaTransactionTimeout = 10 * time.Second

var schemaReferencePattern = regexp.MustCompile("- Reference: `([^`]+)`")

// TestNodeIntegritySchemaReferenceParity sets one issue's state by its
// printed name Done through MCP. It sets another issue's state by the Done
// state UUID through NodeService.Update. Both issues must store the same
// state UUID. A comment that MCP creates on the first issue must store a
// scope_id and appear in the scope_id index.
func TestNodeIntegritySchemaReferenceParity(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), schemaTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	printedID, printedReference := createSchemaIssue(t, harness, "State by name")
	explicitID, _ := createSchemaIssue(t, harness, "State by UUID")
	printed := storedStoreView(t, stores, printedID)
	doneID := schemaStateID(t, stores, printed, "Done")

	updateSchemaState(t, harness, printedID, quoted("Done"))
	nodeService := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter, nil, nil)
	if _, err := nodeService.Update(t.Context(), service.UpdateInput{
		NodeID: explicitID, Name: nil, Props: schemaProps("state_id", quoted(doneID.String())),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("update issue %s state_id to %s: %v", explicitID, doneID, err)
	}
	for _, issueID := range []uuid.UUID{printedID, explicitID} {
		if got := string(storedStoreView(t, stores, issueID).Props["state_id"]); got != strconv.Quote(doneID.String()) {
			t.Fatalf("issue %s state_id = %s, want the Done state %s", issueID, got, doneID)
		}
	}

	arguments := schemaArguments(harness)
	arguments.IssueReference = printedReference
	arguments.Name = "Schema comment"
	commentID := uuid.MustParse(harness.Call(t, "tack_create_comment", arguments).RawID())
	scope := storedStoreView(t, stores, commentID).Props["scope_id"]
	if len(scope) == 0 {
		t.Fatalf("comment %s has no scope_id", commentID)
	}
	comments, err := stores.Nodes.ListByProperty(t.Context(), printed.OrgID, "comment", "scope_id", scope)
	if err != nil {
		t.Fatalf("list comments by scope %s: %v", scope, err)
	}
	if len(comments) != 1 || comments[0].ID != commentID {
		t.Fatalf("scope_id index lists comments %v, want comment %s", comments, commentID)
	}
}

func createSchemaIssue(t *testing.T, harness *integration.MCPHarness, name string) (uuid.UUID, string) {
	t.Helper()
	arguments := schemaArguments(harness)
	arguments.Name = name
	result := harness.Call(t, "tack_create_issue", arguments)
	match := schemaReferencePattern.FindStringSubmatch(result.Text())
	if match == nil {
		t.Fatalf("create issue confirmation has no reference:\n%s", result.Text())
	}
	return uuid.MustParse(result.RawID()), match[1]
}

func updateSchemaState(t *testing.T, harness *integration.MCPHarness, issueID uuid.UUID, state json.RawMessage) {
	t.Helper()
	arguments := schemaArguments(harness)
	arguments.ProjectReference = ""
	arguments.NodeID = issueID.String()
	arguments.Properties = datagen.NodeProperties{"state_id": state}
	harness.Call(t, "tack_update_issue", arguments)
}

func schemaArguments(harness *integration.MCPHarness) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: harness.Project, IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

func schemaStateID(t *testing.T, stores *fdbadapter.Stores, issue *node.NodeView, name string) uuid.UUID {
	t.Helper()
	states, err := stores.Nodes.ListByProperty(t.Context(), issue.OrgID, "state", "parent_id", issue.Props["scope_id"])
	if err != nil {
		t.Fatalf("list states of the project of issue %s: %v", issue.ID, err)
	}
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	t.Fatalf("the project of issue %s has no state %q", issue.ID, name)
	return uuid.Nil
}

func storedStoreView(t *testing.T, stores *fdbadapter.Stores, nodeID uuid.UUID) *node.NodeView {
	t.Helper()
	view, err := stores.Views.Get(t.Context(), nodeID)
	if err != nil || view == nil {
		t.Fatalf("get node %s: view %v, err %v", nodeID, view, err)
	}
	return view
}

func schemaProps(name string, raw json.RawMessage) map[string]json.RawMessage {
	return map[string]json.RawMessage{name: raw}
}

func quoted(value string) json.RawMessage {
	return json.RawMessage(strconv.Quote(value))
}
