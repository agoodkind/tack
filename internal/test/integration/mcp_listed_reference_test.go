package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// listedReferenceCase creates one node through tack_create_<Singular> and
// expects tack_list_<Plural> to print it as "<scope>::<Name>".
type listedReferenceCase struct {
	Singular string
	Plural   string
	// InProject places the node under the harness project, and the project
	// identifier is the scope. Otherwise the workspace slug is the scope.
	InProject bool
	Name      string
	// The second create request sets DuplicateProperty to DuplicateValue. The
	// MCP idempotency key then differs from the first request, and the server
	// creates a second node.
	DuplicateProperty string
	DuplicateValue    string
}

// TestGetByListedScopedNameReference creates a label in the workspace and a
// cycle and a module in a project. Cycles and modules have no sequence. For
// each node, the list tool must print "<scope>::<name>", and the get tool
// with that reference must return the raw ID from the create. After a second
// node with the same name exists in the same scope, the same get must refuse
// with "matched multiple nodes".
func TestGetByListedScopedNameReference(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	cases := []listedReferenceCase{
		{Singular: "label", Plural: "labels", InProject: false, Name: "Listed label", DuplicateProperty: "color", DuplicateValue: "#2A4C6D"},
		{Singular: "cycle", Plural: "cycles", InProject: true, Name: "Listed cycle", DuplicateProperty: "description", DuplicateValue: "Second cycle"},
		{Singular: "module", Plural: "modules", InProject: true, Name: "Listed module", DuplicateProperty: "description", DuplicateValue: "Second module"},
	}
	for _, testCase := range cases {
		t.Run(testCase.Singular, func(t *testing.T) {
			requireListedReferenceResolves(t, harness, testCase)
		})
	}
}

func requireListedReferenceResolves(t *testing.T, harness *integration.MCPHarness, testCase listedReferenceCase) {
	t.Helper()
	scopeArguments := listedReferenceArguments(harness)
	scope := harness.Workspace
	if testCase.InProject {
		scopeArguments.ProjectReference = harness.Project
		scope = harness.Project
	}
	createArguments := scopeArguments
	createArguments.Name = testCase.Name
	nodeID := harness.Call(t, "tack_create_"+testCase.Singular, createArguments).RawID()

	listed := harness.Call(t, "tack_list_"+testCase.Plural, scopeArguments).Text()
	reference := scope + "::" + testCase.Name
	if !strings.Contains(listed, "- `"+reference+"`") {
		t.Fatalf("tack_list_%s does not print reference %q:\n%s", testCase.Plural, reference, listed)
	}
	getArguments := listedReferenceArguments(harness)
	getArguments.NodeID = reference
	if got := harness.Call(t, "tack_get_"+testCase.Singular, getArguments).RawID(); got != nodeID {
		t.Fatalf("tack_get_%s %q = %q, want %q", testCase.Singular, reference, got, nodeID)
	}

	duplicateArguments := createArguments
	duplicateArguments.Properties = datagen.NodeProperties{
		testCase.DuplicateProperty: json.RawMessage(strconv.Quote(testCase.DuplicateValue)),
	}
	if duplicateID := harness.Call(t, "tack_create_"+testCase.Singular, duplicateArguments).RawID(); duplicateID == nodeID {
		t.Fatalf("second tack_create_%s returned the first node %s", testCase.Singular, nodeID)
	}
	message := harness.CallExpectError(t, "tack_get_"+testCase.Singular, getArguments)
	if !strings.Contains(message, "matched multiple nodes") {
		t.Fatalf("tack_get_%s %q with two nodes named %q = %q, want matched multiple nodes",
			testCase.Singular, reference, testCase.Name, message)
	}
}

func listedReferenceArguments(harness *integration.MCPHarness) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}
