package integration_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationRefusesCycles tests cycle refusal for an epic,
// its issue, and the issue's comment. Service requests move the epic under
// itself or the comment. A store plan moves the epic under the issue. Each
// request must fail with domain.ErrInvalidArgument and leave the epic
// unchanged.
func TestNodeIntegrityMutationRefusesCycles(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	top := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	child := createSchemaNode(t, env, top.ID, projectID, "issue", nil)
	grandchild := createSchemaNode(t, env, child.ID, projectID, "comment", schemaProps("description", quoted("Grandchild")))
	before, schema := mutationSnapshot(t, env, top.ID)

	for _, parentID := range []uuid.UUID{top.ID, grandchild.ID} {
		_, err := env.NodeSvc.Update(env.Ctx, moveUpdate(top.ID, parentID))
		requireError(t, err, domain.ErrInvalidArgument, "Update that moves epic "+top.ID.String()+" under "+parentID.String())
	}
	plan := movePlan(t, env, before, schema, child.ID)
	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, plan)
	requireError(t, err, domain.ErrInvalidArgument, "ApplyMutation that moves an epic under its child")
	requireUnchanged(t, env, before)
}

// TestNodeIntegrityMutationSerializesConcurrentMoves checks plans from the
// same state to move epic A under epic B and B under A. The first plan must
// commit, and the second must fail with domain.ErrConflict. Concurrent service
// moves of two other epics under each other must move at most one epic and
// must not create a parent_id cycle.
func TestNodeIntegrityMutationSerializesConcurrentMoves(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	first := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	second := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	firstSnapshot, schema := mutationSnapshot(t, env, first.ID)
	secondSnapshot, _ := mutationSnapshot(t, env, second.ID)
	firstMove := movePlan(t, env, firstSnapshot, schema, second.ID)
	secondMove := movePlan(t, env, secondSnapshot, schema, first.ID)
	if _, err := env.Stores.Nodes.ApplyMutation(env.Ctx, firstMove); err != nil {
		t.Fatalf("move epic %s under %s: %v", first.ID, second.ID, err)
	}
	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, secondMove)
	requireError(t, err, domain.ErrConflict, "ApplyMutation prepared before the opposite move")
	requireUnchanged(t, env, secondSnapshot)

	left := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	right := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	results := make([]error, 2)
	var group sync.WaitGroup
	for position, pair := range [][2]uuid.UUID{{left.ID, right.ID}, {right.ID, left.ID}} {
		group.Go(func() {
			_, results[position] = env.NodeSvc.Update(env.Ctx, moveUpdate(pair[0], pair[1]))
		})
	}
	group.Wait()
	moved := 0
	for _, result := range results {
		switch {
		case result == nil:
			moved++
		case errors.Is(result, domain.ErrConflict), errors.Is(result, domain.ErrInvalidArgument):
		default:
			t.Fatalf("concurrent move returned %v, want success, a conflict, or an invalid argument", result)
		}
	}
	leftParent, _ := mutationSnapshot(t, env, left.ID)
	rightParent, _ := mutationSnapshot(t, env, right.ID)
	if moved > 1 || (leftParent.ParentID == right.ID && rightParent.ParentID == left.ID) {
		t.Fatalf("concurrent moves committed %d moves; parents %s and %s", moved, leftParent.ParentID, rightParent.ParentID)
	}
	requireSingleParentEdge(t, env, left.ID, leftParent.ParentID)
	requireSingleParentEdge(t, env, right.ID, rightParent.ParentID)
}

// requireSingleParentEdge fails unless nodeID has exactly one child_of edge
// and that edge targets parentID.
func requireSingleParentEdge(t *testing.T, env *integration.TestEnv, nodeID, parentID uuid.UUID) {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != parentID {
		t.Fatalf("child_of edges of %s = %v, %v, want one edge to %s", nodeID, edges, err, parentID)
	}
}
