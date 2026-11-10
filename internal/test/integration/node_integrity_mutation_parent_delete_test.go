package integration_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationParentDeletedBeforeMove prepares a plan that moves
// issue A from its epic under issue B. NodeService.Delete deletes B before
// the plan commits. The plan must then fail with domain.ErrConflict or
// domain.ErrInvalidArgument. A must keep its primary record, parent_id equal
// to the epic, and exactly one child_of edge to the epic. B must not exist,
// and no issue parent_id or child_of edge may point at B.
func TestNodeIntegrityMutationParentDeletedBeforeMove(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	moved := createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	target := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before, schema := mutationSnapshot(t, env, moved.ID)
	plan := movePlan(t, env, before, schema, target.ID)

	if _, err := env.NodeSvc.Delete(env.Ctx, target.ID, uuid.New()); err != nil {
		t.Fatalf("delete issue %s: %v", target.ID, err)
	}
	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, plan)
	if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("move of issue %s under deleted issue %s returned %v, want a conflict or an invalid argument",
			moved.ID, target.ID, err)
	}
	requireUnchanged(t, env, before)
	requireStoredParent(t, env, moved.ID, epic.ID)
	requireDeletedParent(t, env, target.ID)
}

// TestNodeIntegrityMutationParentDeletedAfterMove commits a plan that moves
// issue A from its epic under issue B. NodeService.DeleteSubtree then deletes
// B. The issue NodeType lists the project type in CanLiveUnder. The delete
// must move A to the project and delete only B. A must store parent_id equal
// to the project and exactly one child_of edge to the project. B must not
// exist, and no issue parent_id or child_of edge may point at B.
func TestNodeIntegrityMutationParentDeletedAfterMove(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	moved := createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	target := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before, schema := mutationSnapshot(t, env, moved.ID)
	if _, err := env.Stores.Nodes.ApplyMutation(env.Ctx, movePlan(t, env, before, schema, target.ID)); err != nil {
		t.Fatalf("move issue %s under issue %s: %v", moved.ID, target.ID, err)
	}
	requireStoredParent(t, env, moved.ID, target.ID)

	result, err := env.NodeSvc.DeleteSubtree(env.Ctx, env.OrgID, target.ID, uuid.New())
	if err != nil {
		t.Fatalf("delete issue %s: %v", target.ID, err)
	}
	if result.Moved != 1 || result.Deleted != 1 {
		t.Fatalf("delete of issue %s moved %d and deleted %d nodes, want 1 moved and 1 deleted",
			target.ID, result.Moved, result.Deleted)
	}
	requireStoredParent(t, env, moved.ID, projectID)
	requireDeletedParent(t, env, target.ID)
}

// requireStoredParent fails unless nodeID exists with parent_id equal to
// parentID in its primary record and its view, parentID resolves, and nodeID
// has exactly one child_of edge, which targets parentID.
func requireStoredParent(t *testing.T, env *integration.TestEnv, nodeID, parentID uuid.UUID) {
	t.Helper()
	snapshot, _ := mutationSnapshot(t, env, nodeID)
	if !snapshot.Exists || snapshot.ParentID != parentID {
		t.Fatalf("node %s exists %t with parent %s, want parent %s", nodeID, snapshot.Exists, snapshot.ParentID, parentID)
	}
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil || string(view.Props["parent_id"]) != string(quoted(parentID.String())) {
		t.Fatalf("view of node %s = %+v, err %v, want parent_id %s", nodeID, view, err, parentID)
	}
	if resolved, err := env.Stores.Views.Resolve(env.Ctx, parentID); err != nil || resolved == nil {
		t.Fatalf("parent %s of node %s does not resolve: record %v, err %v", parentID, nodeID, resolved, err)
	}
	requireSingleParentEdge(t, env, nodeID, parentID)
}

// requireDeletedParent fails if nodeID still has a primary record or a
// NodeResolve record, or if an issue parent_id or a child_of edge targets
// nodeID.
func requireDeletedParent(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	if snapshot, _ := mutationSnapshot(t, env, nodeID); snapshot.Exists {
		t.Fatalf("node %s still has a primary record after the delete", nodeID)
	}
	if resolved, err := env.Stores.Views.Resolve(env.Ctx, nodeID); resolved != nil || (err != nil && !errors.Is(err, domain.ErrNotFound)) {
		t.Fatalf("node %s after the delete resolves to %+v, err %v, want not found", nodeID, resolved, err)
	}
	children, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "parent_id", quoted(nodeID.String()))
	if err != nil || len(children) != 0 {
		t.Fatalf("issues with parent_id %s after the delete = %d, err %v, want none", nodeID, len(children), err)
	}
	edges, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil || len(edges) != 0 {
		t.Fatalf("child_of edges to %s after the delete = %v, err %v, want none", nodeID, edges, err)
	}
}
