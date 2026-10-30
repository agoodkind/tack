package integration_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// strictToolErrorMarker is the text that the datagen driver puts in an error
// for a tools/call result with isError set.
const strictToolErrorMarker = "tack_update_issue tool error"

// TestNodeIntegrityStrictMCPRejectsInvalidUpdatesAfterActivation creates an
// issue through tack_create_issue, activates strict validation, and then calls
// tack_update_issue through the production MCP HTTP handler with each invalid
// property request in the table. Each call must return a tool error. Each
// refused call must leave the issue's primary record and outgoing
// relationships unchanged.
func TestNodeIntegrityStrictMCPRejectsInvalidUpdatesAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	harness := integration.NewMCPHarness(t)
	issueID, _ := createSchemaIssue(t, harness, "Strict MCP issue")
	otherProjectID := createStrictOtherProject(t, harness)
	activateStrict(t)

	cases := []struct {
		rule       string
		properties datagen.NodeProperties
	}{
		{rule: "undeclared property", properties: datagen.NodeProperties{"strict_test_undeclared": quoted("value")}},
		{rule: "option label instead of key", properties: datagen.NodeProperties{"priority": quoted("High")}},
		{rule: "wrong JSON type", properties: datagen.NodeProperties{"priority": json.RawMessage(`5`)}},
		{rule: "required property removal", properties: datagen.NodeProperties{"state_id": json.RawMessage(`null`)}},
		{rule: "client scope_id", properties: datagen.NodeProperties{"scope_id": quoted(otherProjectID)}},
		{rule: "client sequence", properties: datagen.NodeProperties{"sequence": json.RawMessage(strictManagedSequence)}},
	}
	for _, rule := range cases {
		t.Run(rule.rule, func(t *testing.T) {
			before := strictHarnessSnapshot(t, env, harness, issueID)
			arguments := schemaArguments(harness)
			arguments.ProjectReference = ""
			arguments.NodeID = issueID.String()
			arguments.Properties = rule.properties
			refusal := harness.CallExpectError(t, "tack_update_issue", arguments)
			if !strings.Contains(refusal, strictToolErrorMarker) {
				t.Fatalf("tack_update_issue with a %s = %q, want a tool error", rule.rule, refusal)
			}
			after := strictHarnessSnapshot(t, env, harness, issueID)
			if after.PrimaryDigest != before.PrimaryDigest {
				t.Fatalf("the refused tack_update_issue with a %s changed issue %s", rule.rule, issueID)
			}
			if !reflect.DeepEqual(after.Outgoing, before.Outgoing) {
				t.Fatalf("the refused tack_update_issue with a %s changed the relationships of issue %s", rule.rule, issueID)
			}
		})
	}
}

// createStrictOtherProject creates a second project in the harness workspace
// through tack_create_project and returns its ID.
func createStrictOtherProject(t *testing.T, harness *integration.MCPHarness) string {
	t.Helper()
	arguments := schemaArguments(harness)
	arguments.ProjectReference = ""
	arguments.Name = "Strict other project"
	arguments.Properties = datagen.NodeProperties{"identifier": quoted("STRICTOTHER")}
	return harness.Call(t, "tack_create_project", arguments).RawID()
}

// strictHarnessSnapshot reads the mutation snapshot of nodeID in the harness
// organization.
func strictHarnessSnapshot(t *testing.T, env *integration.TestEnv, harness *integration.MCPHarness, nodeID uuid.UUID) node.MutationSnapshot {
	t.Helper()
	snapshot, _, err := env.Stores.Nodes.ReadMutationSnapshot(env.Ctx, harness.OrgID(), nodeID)
	if err != nil || !snapshot.Exists {
		t.Fatalf("read the mutation snapshot of %s: exists %t, err %v", nodeID, snapshot.Exists, err)
	}
	return snapshot
}
