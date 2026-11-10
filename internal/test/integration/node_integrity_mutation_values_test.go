package integration_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// refusedOption is a priority value that does not match any option key in
// the priority definition.
const refusedOption = "critical"

// TestNodeIntegrityMutationRejectsInvalidValues calls NodeService.Create
// with an undeclared priority option. The call must fail and must not store
// an issue. The test then creates an issue and calls NodeService.Update with
// each value in the table. Each update must return an error that wraps
// domain.ErrInvalidArgument and contains the property. Each update must not
// change the stored issue.
func TestNodeIntegrityMutationRejectsInvalidValues(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	_, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(projectID, projectID, "issue", schemaProps("priority", quoted(refusedOption))))
	requireRefusedProperty(t, err, "priority")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("the refused create stored %d issues", len(issues))
	}

	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before, _ := mutationSnapshot(t, env, issue.ID)
	for _, request := range []struct {
		property string
		value    json.RawMessage
	}{
		{"priority", quoted(refusedOption)},
		{"due_date", quoted("tomorrow")},
		{"state_id", json.RawMessage("null")},
		{"state_id", quoted(projectID.String())},
		{"state_id", quoted(uuid.Must(uuid.NewV7()).String())},
	} {
		_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
			NodeID: issue.ID, Name: nil, Props: schemaProps(request.property, request.value),
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
		})
		requireRefusedProperty(t, err, request.property)
		requireUnchanged(t, env, before)
	}
}

// TestNodeIntegrityMutationMCPRejectsInvalidOption calls tack_create_issue
// and tack_update_issue with an undeclared priority option. Both calls must
// fail. The create call must not store an issue, and the update call must not
// change the stored issue.
func TestNodeIntegrityMutationMCPRejectsInvalidOption(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), schemaTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	arguments := schemaArguments(harness)
	arguments.Name = "Refused option issue"
	arguments.Properties = datagen.NodeProperties{"priority": quoted(refusedOption)}
	harness.CallExpectError(t, "tack_create_issue", arguments)
	listed := harness.Call(t, "tack_list_issues", schemaArguments(harness)).Text()
	if strings.Contains(listed, arguments.Name) {
		t.Fatalf("the refused create stored an issue:\n%s", listed)
	}

	issueID, _ := createSchemaIssue(t, harness, "Kept option issue")
	before := storedStoreView(t, stores, issueID)
	update := schemaArguments(harness)
	update.ProjectReference = ""
	update.NodeID = issueID.String()
	update.Properties = datagen.NodeProperties{"priority": quoted(refusedOption)}
	harness.CallExpectError(t, "tack_update_issue", update)
	after := storedStoreView(t, stores, issueID)
	if string(after.Props["priority"]) != string(before.Props["priority"]) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("the refused update changed issue %s: priority %s to %s", issueID, before.Props["priority"], after.Props["priority"])
	}
}

// requireRefusedProperty fails if err does not wrap domain.ErrInvalidArgument
// or does not contain properties.<property>.
func requireRefusedProperty(t *testing.T, err error, property string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), "properties."+property) {
		t.Fatalf("error = %v, want an invalid argument that contains properties.%s", err, property)
	}
}
