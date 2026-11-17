package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// moveOpaqueNode reads nodeID and fails the test unless its type matches
// kind.TypeKey and its child_of edge targets from. It calls NodeService.Update
// to replace child_of and set scope_id to to in one transaction.
// Every caller passes a workspace entry node as to.
func moveOpaqueNode(
	t *testing.T,
	fixture queryFixture,
	kind opaqueKind,
	nodeID uuid.UUID,
	from uuid.UUID,
	to uuid.UUID,
) {
	t.Helper()
	current, err := fixture.Stores.Views.Get(WithTestOperation(t, t.Context()), nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s before the move: %v", nodeID, err)
	}

	parents, err := fixture.Stores.Relationships.ListBySource(
		WithTestOperation(t, t.Context()),
		current.OrgID,
		nodeID,
		node.RelChildOf,
	)
	if err != nil || current.NodeType != kind.TypeKey || len(parents) != 1 ||
		parents[0].TargetID != from {
		t.Fatalf(
			"node %s has type %s and child_of edges %v, error %v; want type %s under %s",
			nodeID,
			current.NodeType,
			parents,
			err,
			kind.TypeKey,
			from,
		)
	}

	if _, err := fixtureNodeService(t, fixture).Update(
		WithTestOperation(t, t.Context()),
		service.UpdateInput{
			NodeID:  nodeID,
			Props:   nil,
			ScopeID: to,
			RelationshipChanges: node.RelationshipChanges{
				Add: []*node.Relationship{
					{
						OrgID:        current.OrgID,
						SourceID:     nodeID,
						RelationType: node.RelChildOf,
						TargetID:     to,
					},
				},
				Remove: nil,
			},
		},
	); err != nil {
		t.Fatalf("move node %s from %s to %s: %v", nodeID, from, to, err)
	}
}

// drainSearchWork runs the production worker until no work is claimable.
func drainSearchWork(t *testing.T, worker *service.SearchWorker, maxSlices int) {
	t.Helper()
	runSearchWorkerWithin(t, worker, maxSlices)
}
