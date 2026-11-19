package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// removeRepairParentEdges creates an incomplete legacy graph through the
// unchecked store update before strict activation.
func removeRepairParentEdges(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read repair fixture node %s: %v", nodeID, err)
	}

	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read repair fixture view %s: %v", nodeID, err)
	}

	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("read repair fixture parent edges of %s: %v", nodeID, err)
	}

	if err := env.Stores.Nodes.UpdateAtomic(
		env.Ctx,
		current,
		view,
		current.Props,
		nil,
		nil,
		node.RelationshipChanges{Remove: edges},
	); err != nil {
		t.Fatalf("remove repair fixture parent edges of %s: %v", nodeID, err)
	}
}
