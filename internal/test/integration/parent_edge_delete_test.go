package integration_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeDeleteMovesChildIssue checks subtree deletion of a parent
// issue with a child issue under it, a type pair that NodeType metadata does
// not declare. Subtree deletion moves the child issue to the project with one
// child_of edge to the project and a matching parent_id, and deletes the
// comment with the parent issue.
func TestParentEdgeDeleteMovesChildIssue(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "DELMOVE")
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
	if err != nil || moved == nil || string(moved.Props["parent_id"]) != string(parentEdgeJSON(t, project.String())) {
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

// TestParentEdgeDeleteRefusesTwoParents creates a project with four issues
// through the node service: a second parent issue, a root issue, a parent
// issue, and a child issue under the parent issue. The relationship store
// adds a second child_of edge from the root issue to the second parent issue
// and another from the child issue to the second parent issue. The test
// requires both subtree deletes, one for the root issue and one for the
// parent issue, to return errors that match node.ErrNoHierarchyParent. The
// test requires all four issues to remain.
func TestParentEdgeDeleteRefusesTwoParents(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "DELTWO")
	second := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Second parent", nil)
	root := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Issue with two parents", nil)
	parent := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Parent issue", nil)
	child := createDeleteFixtureNode(t, env, actor, parent, project, "issue", "Child with two parents", nil)
	for _, source := range []uuid.UUID{root, child} {
		edge := &node.Relationship{OrgID: env.OrgID, SourceID: source, RelationType: node.RelChildOf, TargetID: second}
		if err := env.Stores.Relationships.Add(env.Ctx, edge); err != nil {
			t.Fatalf("add second parent edge of issue %s: %v", source, err)
		}
	}
	for _, deleted := range []uuid.UUID{root, parent} {
		if _, err := env.NodeSvc.DeleteSubtree(env.Ctx, env.OrgID, deleted, actor, nil); !errors.Is(err, node.ErrNoHierarchyParent) {
			t.Fatalf("delete of issue %s returned %v, want an error that matches node.ErrNoHierarchyParent", deleted, err)
		}
	}
	for _, kept := range []uuid.UUID{second, root, parent, child} {
		if view, err := env.Stores.Views.Get(env.Ctx, kept); err != nil || view == nil {
			t.Fatalf("issue %s after the refused deletes = %+v, err %v, want it present", kept, view, err)
		}
	}
}

func createDeleteFixtureProject(t *testing.T, env *integration.TestEnv, actor uuid.UUID, identifier string) uuid.UUID {
	t.Helper()
	workspace := createDeleteFixtureNode(t, env, actor, env.OrgID, env.OrgID, "workspace", "Workspace "+identifier,
		map[string]json.RawMessage{"slug": parentEdgeJSON(t, "workspace-"+strings.ToLower(identifier))})
	return createDeleteFixtureNode(t, env, actor, workspace, workspace, "project", "Project "+identifier,
		map[string]json.RawMessage{"identifier": parentEdgeJSON(t, identifier)})
}

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
