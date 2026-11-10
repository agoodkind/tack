package integration_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationRefusesDeleteOfReferencedState creates an issue
// that stores the Todo state in state_id, then calls NodeService.Delete on
// the Todo state. The delete call must return an error that wraps
// domain.ErrFailedPrecondition and must not change the state.
func TestNodeIntegrityMutationRefusesDeleteOfReferencedState(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	createSchemaNode(t, env, projectID, projectID, "issue", nil)
	todoID := stateByName(t, env, projectID, "Todo")
	before, _ := mutationSnapshot(t, env, todoID)

	_, err := env.NodeSvc.Delete(env.Ctx, todoID, uuid.New())
	requireError(t, err, domain.ErrFailedPrecondition, "the delete of referenced state "+todoID.String())
	requireUnchanged(t, env, before)
}

// TestNodeIntegrityMutationRefusesUnindexedReferenceDefinition calls
// PropertyDefStore.Set with a UUID property definition that has a reference
// target type and sets Indexed to false. The write must return an error that
// wraps domain.ErrInvalidArgument and must not store the definition.
func TestNodeIntegrityMutationRefusesUnindexedReferenceDefinition(t *testing.T) {
	env := integration.SetupTestEnv(t)
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: "reviewer_state_id", Type: node.PropertyTypeUUID,
		Indexed: false, ReferenceTargetTypeKey: "state",
		Search: &node.SearchProjection{Include: false, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	err := env.Stores.PropertyDefs.Set(env.Ctx, definition)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("store the unindexed reference definition returned %v, want an invalid argument", err)
	}
	stored, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, definition.ID)
	if err != nil || stored != nil {
		t.Fatalf("definition %s after the refused write = %+v, %v, want none", definition.ID, stored, err)
	}
}
