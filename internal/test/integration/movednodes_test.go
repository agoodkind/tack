package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// moveRolloutNode replaces nodeID's child_of edge and sets scope_id to to.
func moveRolloutNode(t *testing.T, fixture rolloutFixture, nodeID, to uuid.UUID) {
	t.Helper()
	if _, err := rolloutNodeService(fixture).Update(
		rolloutContext(t),
		service.UpdateInput{
			NodeID: nodeID,
			RelationshipChanges: node.RelationshipChanges{
				Add: []*node.Relationship{{RelationType: node.RelChildOf, TargetID: to}},
			},
			ScopeID: to,
		},
	); err != nil {
		t.Fatalf("cannot move node %s under entry node %s: %v", nodeID, to, err)
	}
}
