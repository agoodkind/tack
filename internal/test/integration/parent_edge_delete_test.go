package integration_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeDeleteMovesChildIssue creates an issue under a project, a
// child issue under that issue, and a comment under that issue through the
// node service. No metadata declares an issue under an issue. The subtree
// delete of the issue must move the child issue to the project, with one
// child_of edge to the project and a parent_id that matches it, and must
// delete the comment with the issue.
func TestParentEdgeDeleteMovesChildIssue(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	workspace := createDeleteFixtureNode(t, env, actor, env.OrgID, env.OrgID, "workspace", "Delete workspace",
		map[string]json.RawMessage{"slug": parentEdgeJSON(t, "delete-workspace")})
	project := createDeleteFixtureNode(t, env, actor, workspace, workspace, "project", "Delete project",
		map[string]json.RawMessage{"identifier": parentEdgeJSON(t, "DELMOVE")})
	issue := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Deleted issue", nil)
	child := createDeleteFixtureNode(t, env, actor, issue, project, "issue", "Child issue", nil)
	comment := createDeleteFixtureNode(t, env, actor, issue, project, "comment", "Comment on the deleted issue", nil)

	result, err := env.NodeSvc.DeleteSubtree(env.Ctx, env.OrgID, issue, actor, nil)
	if err != nil {
		t.Fatalf("delete issue %s: %v", issue, err)
	}
	if result.Moved != 1 || result.Deleted != 2 {
		t.Fatalf("delete of issue %s moved %d and deleted %d nodes, want 1 moved and 2 deleted", issue, result.Moved, result.Deleted)
	}
	moved, err := env.Stores.Views.Get(env.Ctx, child)
	if err != nil || moved == nil || node.ParentIDText(moved.Props) != project.String() {
		t.Fatalf("child issue %s after the delete = %+v, err %v, want parent_id %s", child, moved, err, project)
	}
	edges, err := env.Stores.Relationships.EdgesFrom(env.Ctx, env.OrgID, child, node.RelChildOf, "", 10)
	if err != nil || len(edges.IDs) != 1 || edges.IDs[0] != project {
		t.Fatalf("child_of edges of child issue %s = %v, err %v, want only project %s", child, edges.IDs, err, project)
	}
	for _, deleted := range []uuid.UUID{issue, comment} {
		view, err := env.Stores.Views.Get(env.Ctx, deleted)
		if view != nil || (err != nil && !errors.Is(err, domain.ErrNotFound)) {
			t.Fatalf("node %s after the delete = %+v, err %v, want it deleted", deleted, view, err)
		}
	}
}

// createDeleteFixtureNode creates one node of typeKey under parentID through
// NodeService.Create and returns its ID.
func createDeleteFixtureNode(
	t *testing.T, env *integration.TestEnv, actor, parentID, scopeID uuid.UUID, typeKey, name string, props map[string]json.RawMessage,
) uuid.UUID {
	t.Helper()
	created, err := env.NodeSvc.Create(env.Ctx, service.CreateInput{
		ParentID: parentID, ScopeID: scopeID, NodeTypeKey: typeKey, Name: name, Props: props, ActorID: actor,
	})
	if err != nil || created == nil || created.View == nil {
		t.Fatalf("create %s %q: %v", typeKey, name, err)
	}
	return created.View.ID
}
