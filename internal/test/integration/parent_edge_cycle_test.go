package integration_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeUpdateRefusesCyclicAncestry stores parent_id values that form
// a cycle between two issues through the node store, which does not check
// parents. It requires the node service to refuse a third issue's move under
// one of those issues with an error that matches domain.ErrInvalidArgument
// and contains both node IDs. After the refusal, the third issue has the
// project as its parent_id.
func TestParentEdgeUpdateRefusesCyclicAncestry(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "CYCLE")
	first := createDeleteFixtureNode(t, env, actor, project, project, "issue", "First cycle issue", nil)
	second := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Second cycle issue", nil)
	moved := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Moved issue", nil)
	storeParentID(t, env, first, second)
	storeParentID(t, env, second, first)

	_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: moved, Props: map[string]json.RawMessage{"parent_id": parentEdgeJSON(t, first.String())}, ActorID: actor,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), moved.String()) || !strings.Contains(err.Error(), first.String()) {
		t.Fatalf("move of issue %s under %s returned %v, want a refusal with both IDs", moved, first, err)
	}
	view, err := env.Stores.Views.Get(env.Ctx, moved)
	if err != nil || view == nil || string(view.Props["parent_id"]) != string(parentEdgeJSON(t, project.String())) {
		t.Fatalf("issue %s after the refused move = %+v, err %v, want parent_id %s", moved, view, err, project)
	}
}

// storeParentID writes parentID as the parent_id of nodeID in the primary
// record and the view through NodeStore.Set without changing child_of edges.
func storeParentID(t *testing.T, env *integration.TestEnv, nodeID, parentID uuid.UUID) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read view of node %s: %v", nodeID, err)
	}
	value := parentEdgeJSON(t, parentID.String())
	current.Props["parent_id"], view.Props["parent_id"] = value, value
	if err := env.Stores.Nodes.Set(env.Ctx, current, view); err != nil {
		t.Fatalf("store parent_id %s on node %s: %v", parentID, nodeID, err)
	}
}
