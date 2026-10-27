package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictSubtreeDeleteAfterActivation verifies subtree
// deletion after strict validation activation. It creates a project, an epic
// under the project, an issue under the epic, and a comment under the epic.
// It activates strict validation and deletes the epic through
// NodeService.Delete. The issue type can live under a project, and the
// comment type cannot.
//
// The test requires a finished job that moved exactly one child. It checks
// that the moved issue stores parent_id equal to the project and exactly one
// child_of edge to the project. It verifies that storage does not contain
// the epic or the comment.
func TestNodeIntegrityStrictSubtreeDeleteAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	issue := createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	comment := createSchemaNode(t, env, epic.ID, projectID, "comment", schemaProps("description", quoted("A comment on the epic")))
	activateStrict(t)

	result, err := env.NodeSvc.Delete(env.Ctx, epic.ID, uuid.New())
	if err != nil || result.State != node.SubtreeDeleteFinished || result.Moved != 1 {
		t.Fatalf("delete of epic %s after activation = %+v, %v; want a finished job that moved one child", epic.ID, result, err)
	}
	if parentID := storedParentID(t, env, issue.ID); parentID != projectID {
		t.Fatalf("parent_id of moved issue %s = %s, want project %s", issue.ID, parentID, projectID)
	}
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != projectID {
		t.Fatalf("child_of edges of moved issue %s = %v, %v, want one edge to project %s", issue.ID, edges, err, projectID)
	}
	for name, nodeID := range map[string]uuid.UUID{"epic": epic.ID, "comment": comment.ID} {
		stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
		if err != nil || stored != nil {
			t.Fatalf("%s %s after the delete = %+v, %v; want no stored node", name, nodeID, stored, err)
		}
	}
}
