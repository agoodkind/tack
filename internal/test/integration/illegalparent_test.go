package integration_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeCreateRejectsParentType checks the parent metadata through NodeService.Create.
func TestNodeCreateRejectsParentType(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	stateID := stateByName(t, env, projectID, "Todo")
	before, _ := mutationSnapshot(t, env, stateID)
	_, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(stateID, projectID, "issue", nil))
	requireError(t, err, domain.ErrInvalidArgument, "create an issue under a state")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("the refused create stored %d issues", len(issues))
	}

	after, _ := mutationSnapshot(t, env, stateID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused create changed parent state %s", stateID)
	}

	createSchemaNode(t, env, projectID, projectID, "issue", nil)
}

// TestNodeUpdateRejectsMissingParent checks a raw UUID through NodeService.Update.
func TestNodeUpdateRejectsMissingParent(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before, _ := mutationSnapshot(t, env, issue.ID)
	_, err := env.NodeSvc.Update(env.Ctx, moveUpdate(issue.ID, uuid.New()))
	requireError(t, err, domain.ErrInvalidArgument, "update an issue with a missing parent")
	after, _ := mutationSnapshot(t, env, issue.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused parent update changed issue %s", issue.ID)
	}
}
