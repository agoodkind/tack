package integration

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// moveOpaqueNode reads nodeID and fails the test unless its type matches
// kind.TypeKey and its parent_id equals from. It calls NodeService.Update
// with both parent_id and scope_id set to to. Every caller passes a workspace
// entry node as to. The update replaces the child_of edge in the same
// transaction.
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

	if current.NodeType != kind.TypeKey ||
		!bytes.Equal(current.Props["parent_id"], mustJSON(from.String())) {
		t.Fatalf(
			"node %s has type %s and parent_id %s, want type %s under %s",
			nodeID,
			current.NodeType,
			current.Props["parent_id"],
			kind.TypeKey,
			from,
		)
	}

	if _, err := fixtureNodeService(t, fixture).Update(
		WithTestOperation(t, t.Context()),
		service.UpdateInput{
			NodeID:  nodeID,
			Props:   map[string]json.RawMessage{"parent_id": mustJSON(to.String())},
			ScopeID: to,
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
