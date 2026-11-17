package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeUpdateRefusesCyclicAncestry stores child_of edges that form
// a cycle between two issues through the node store, which does not check
// cycles. It requires the node service to refuse a third issue's move under
// one of those issues with an error that matches domain.ErrInvalidArgument
// and contains both node IDs. After the refusal, the third issue has the
// project as its child_of target.
func TestParentEdgeUpdateRefusesCyclicAncestry(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "CYCLE")
	first := createDeleteFixtureNode(
		t,
		env,
		actor,
		project,
		project,
		"issue",
		"First cycle issue",
		nil,
	)
	second := createDeleteFixtureNode(
		t,
		env,
		actor,
		project,
		project,
		"issue",
		"Second cycle issue",
		nil,
	)
	moved := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Moved issue", nil)
	storeParentID(t, env, first, second.String())
	storeParentID(t, env, second, first.String())

	_, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID: moved,
			RelationshipChanges: node.RelationshipChanges{
				Add: []*node.Relationship{
					{SourceID: moved, RelationType: node.RelChildOf, TargetID: first},
				},
			},
			ActorID: actor,
		},
	)
	if !errors.Is(err, domain.ErrInvalidArgument) ||
		!strings.Contains(err.Error(), moved.String()) ||
		!strings.Contains(err.Error(), first.String()) {
		t.Fatalf(
			"move of issue %s under %s returned %v, want a refusal with both IDs",
			moved,
			first,
			err,
		)
	}

	requireStoredParent(t, env, moved, project)
}

// storeParentID replaces child_of edges through the relationship store.
func storeParentID(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID, parentValue string) {
	t.Helper()
	parentID := uuid.MustParse(parentValue)
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("read parents of node %s: %v", nodeID, err)
	}

	if err := env.Stores.Relationships.Add(
		env.Ctx,
		&node.Relationship{
			OrgID:        env.OrgID,
			SourceID:     nodeID,
			RelationType: node.RelChildOf,
			TargetID:     parentID,
		},
	); err != nil {
		t.Fatalf("store parent %s of node %s: %v", parentID, nodeID, err)
	}

	for _, edge := range edges {
		if edge.TargetID != parentID {
			if err := env.Stores.Relationships.Remove(
				env.Ctx,
				env.OrgID,
				nodeID,
				node.RelChildOf,
				edge.TargetID,
			); err != nil {
				t.Fatalf("remove parent %s of node %s: %v", edge.TargetID, nodeID, err)
			}
		}
	}
}
