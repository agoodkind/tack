package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityScopeCommandRequiresParentChange calls NodeService.Update
// with UpdateInput.ScopeID set to another project. The update must fail with
// an error that wraps domain.ErrInvalidArgument and must leave the issue
// unchanged when child_of repeats the stored parent and when the update does
// not request a parent. An update that moves the issue under the other
// project with the same ScopeID must succeed and store that project as both
// child_of target and scope_id.
func TestNodeIntegrityScopeCommandRequiresParentChange(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	workspaceID := storedParentID(t, env, projectID)
	otherProject := createSchemaNode(
		t,
		env,
		workspaceID,
		workspaceID,
		"project",
		map[string]json.RawMessage{
			"slug":       quoted("other"),
			"identifier": quoted("OTHER"),
		},
	)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	scopeUpdate := func(parentID uuid.UUID) service.UpdateInput {
		var relationships []*node.Relationship
		if parentID != uuid.Nil {
			relationships = []*node.Relationship{
				{RelationType: node.RelChildOf, TargetID: parentID},
			}
		}

		return service.UpdateInput{
			NodeID: issue.ID,
			Name:   nil,
			Props:  nil,
			RelationshipChanges: node.RelationshipChanges{
				Add:    relationships,
				Remove: nil,
			},
			ActorID: uuid.New(),
			ScopeID: otherProject.ID,
		}
	}

	refused := map[string]uuid.UUID{
		"the stored parent": projectID,
		"no parent":         uuid.Nil,
	}
	for name, parentID := range refused {
		before, _ := mutationSnapshot(t, env, issue.ID)
		_, err := env.NodeSvc.Update(env.Ctx, scopeUpdate(parentID))
		requireError(t, err, domain.ErrInvalidArgument, "the scope change with "+name)
		requireUnchanged(t, env, before)
	}

	if _, err := env.NodeSvc.Update(env.Ctx, scopeUpdate(otherProject.ID)); err != nil {
		t.Fatalf("move issue %s to project %s with its scope: %v", issue.ID, otherProject.ID, err)
	}

	moved, _ := mutationSnapshot(t, env, issue.ID)
	scope := string(moved.Node.Props["scope_id"])
	if moved.ParentID != otherProject.ID || scope != strconv.Quote(otherProject.ID.String()) {
		t.Fatalf(
			"moved issue %s has parent %s and scope_id %s, want project %s for both",
			issue.ID,
			moved.ParentID,
			scope,
			otherProject.ID,
		)
	}
}
