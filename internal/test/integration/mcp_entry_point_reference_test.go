package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// entryPointLabelName is the name of both labels. NewMCPHarness gives each
// test its own workspace.
const entryPointLabelName = "Entry point label"

// TestGetLabelByListedReference creates a label, lists it through
// tack_list_labels, and gets it through tack_get_label by the printed
// reference. The get must return the raw ID from the create. After a second
// label with the same name exists in the workspace, the same get must refuse
// with "matched multiple nodes".
func TestGetLabelByListedReference(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	name := entryPointLabelName
	createArguments := entryPointReferenceArguments(harness)
	createArguments.Name = name
	labelID := harness.Call(t, "tack_create_label", createArguments).RawID()

	listed := harness.Call(t, "tack_list_labels", entryPointReferenceArguments(harness)).Text()
	reference := harness.Workspace + "::" + name
	if !strings.Contains(listed, "- `"+reference+"`") {
		t.Fatalf("tack_list_labels does not print reference %q:\n%s", reference, listed)
	}
	getArguments := entryPointReferenceArguments(harness)
	getArguments.NodeID = reference
	if got := harness.Call(t, "tack_get_label", getArguments).RawID(); got != labelID {
		t.Fatalf("tack_get_label %q = %q, want %q", reference, got, labelID)
	}

	duplicateArguments := createArguments
	duplicateArguments.Properties = datagen.NodeProperties{"color": json.RawMessage(strconv.Quote("#2A4C6D"))}
	duplicateID := harness.Call(t, "tack_create_label", duplicateArguments).RawID()
	if duplicateID == labelID {
		t.Fatalf("second tack_create_label returned the first label %s", labelID)
	}
	message := harness.CallExpectError(t, "tack_get_label", getArguments)
	if !strings.Contains(message, "matched multiple nodes") {
		t.Fatalf("tack_get_label %q with two labels named %q = %q, want matched multiple nodes", reference, name, message)
	}
}

func entryPointReferenceArguments(harness *integration.MCPHarness) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}
