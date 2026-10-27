package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentReferenceParityMovesIssues creates an epic and two issues through
// MCP tools. tack_update_issue moves the first issue under the epic by the
// printed epic reference and the second issue by the raw epic UUID. Both
// issues must store parent_id equal to the epic UUID and exactly one child_of
// edge to the epic. Both issues must keep NodeResolve records with the epic's
// organization and the issue node type, and both must store the same
// scope_id.
func TestParentReferenceParityMovesIssues(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	epicID, epicReference := createReferencedNode(t, harness, "tack_create_epic", "Parity parent epic")
	printedID, _ := createSchemaIssue(t, harness, "Moved by printed reference")
	rawID, _ := createSchemaIssue(t, harness, "Moved by raw UUID")

	harness.Call(t, "tack_update_issue", parentMoveArguments(harness, printedID, epicReference))
	harness.Call(t, "tack_update_issue", parentMoveArguments(harness, rawID, epicID.String()))

	epic := epicID.String()
	requireParents(t, stores, printedID.String(), epic, []string{epic})
	requireParents(t, stores, rawID.String(), epic, []string{epic})
	epicResolve := storedResolve(t, stores, epicID)
	for _, issueID := range []uuid.UUID{printedID, rawID} {
		resolved := storedResolve(t, stores, issueID)
		if resolved.OrgID != epicResolve.OrgID || resolved.NodeType != "issue" {
			t.Errorf("issue %s resolves to organization %s and type %q, want organization %s and type issue",
				issueID, resolved.OrgID, resolved.NodeType, epicResolve.OrgID)
		}
	}
	printedScope := stringProperty(t, storedStoreView(t, stores, printedID), "scope_id")
	rawScope := stringProperty(t, storedStoreView(t, stores, rawID), "scope_id")
	if printedScope == "" || printedScope != rawScope {
		t.Fatalf("issue %s has scope_id %q and issue %s has scope_id %q, want the same scope",
			printedID, printedScope, rawID, rawScope)
	}
}

// TestParentReferenceParityRefusesStateParent creates an issue in a project
// through MCP tools and reads the Done state of that project, a type that
// the issue type cannot live under. tack_update_issue must refuse a move of
// the issue under the printed state reference and under the raw state UUID
// with an error that names parent_id. After each refusal, the issue must
// keep parent_id equal to the project, exactly one child_of edge to the
// project, and its stored update time.
func TestParentReferenceParityRefusesStateParent(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	issueID, _ := createSchemaIssue(t, harness, "Issue kept under the project")
	before := storedStoreView(t, stores, issueID)
	projectID := stringProperty(t, before, "parent_id")
	stateID := schemaStateID(t, stores, before, "Done")
	stateReference := harness.Project + "::Done"
	lookup := schemaArguments(harness)
	lookup.ProjectReference = ""
	lookup.NodeID = stateReference
	if got := harness.Call(t, "tack_get_state", lookup).RawID(); got != stateID.String() {
		t.Fatalf("tack_get_state %q returned %s, want the Done state %s", stateReference, got, stateID)
	}

	for _, reference := range []string{stateReference, stateID.String()} {
		text := harness.CallExpectError(t, "tack_update_issue", parentMoveArguments(harness, issueID, reference))
		if !strings.Contains(text, "parent_id") {
			t.Fatalf("move of issue %s under state %q returned %q, want a parent_id refusal", issueID, reference, text)
		}
		requireParents(t, stores, issueID.String(), projectID, []string{projectID})
		if after := storedStoreView(t, stores, issueID); !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("refused move of issue %s under state %q changed its update time from %s to %s",
				issueID, reference, before.UpdatedAt, after.UpdatedAt)
		}
	}
}

// createReferencedNode calls the create tool with name in the harness project
// and returns the raw ID and the printed reference from the confirmation.
func createReferencedNode(t *testing.T, harness *integration.MCPHarness, tool, name string) (uuid.UUID, string) {
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
func parentMoveArguments(harness *integration.MCPHarness, issueID uuid.UUID, reference string) datagen.ToolArguments {
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
