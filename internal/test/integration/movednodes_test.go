package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// moveRolloutNode calls [service.NodeService.Update] to set nodeID's
// parent_id and scope_id to the entry node that `to` identifies.
// The update replaces the node's child_of edge and changes a resource grant.
func moveRolloutNode(t *testing.T, fixture rolloutFixture, nodeID, to uuid.UUID) {
	t.Helper()
	target, err := json.Marshal(to.String())
	if err != nil {
		t.Fatalf("cannot encode property value: %v", err)
	}

	if _, err := rolloutNodeService(fixture).Update(
		rolloutContext(t),
		service.UpdateInput{
			NodeID:  nodeID,
			Props:   map[string]json.RawMessage{"parent_id": target},
			ScopeID: to,
		},
	); err != nil {
		t.Fatalf("cannot move node %s under entry node %s: %v", nodeID, to, err)
	}
}
