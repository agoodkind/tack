package integration

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestNodeServiceUpdateRefusesTextParent pins the service half of TACK-523
// for callers that bypass the MCP tools: a parent_id that is not the id of a
// node in the org is refused before the write, and the node keeps its parent.
func TestNodeServiceUpdateRefusesTextParent(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	issueID := createTestIssue(t, env, projectID, "text parent refused")

	_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID:              issueID,
		Name:                nil,
		Props:               map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote("TACK-517"))},
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		ActorID:             uuid.New(),
	})

	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Update with a text parent_id returned %v, want invalid argument", err)
	}
	view, err := env.Stores.Views.Get(env.Ctx, issueID)
	if err != nil || view == nil {
		t.Fatalf("read the issue after the refused update: %v", err)
	}
	var parent string
	if err := json.Unmarshal(view.Props["parent_id"], &parent); err != nil || parent != projectID.String() {
		t.Fatalf("parent_id after the refused update = %s, want %s", view.Props["parent_id"], projectID)
	}
}

// TestNodeServiceUpdateMovesChildOfEdge checks that a valid parent change
// moves the child_of edge in the same write.
func TestNodeServiceUpdateMovesChildOfEdge(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	issueID := createTestIssue(t, env, projectID, "moved issue")
	epic := mustCreate(t, env, service.CreateInput{
		ParentID: projectID, ScopeID: projectID, NodeTypeKey: "epic", Name: "target epic", ActorID: uuid.New(),
	})

	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID:              issueID,
		Name:                nil,
		Props:               map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote(epic.ID.String()))},
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		ActorID:             uuid.New(),
	}); err != nil {
		t.Fatalf("Update with the epic as parent: %v", err)
	}

	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issueID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges: %v", err)
	}
	if len(edges) != 1 || edges[0].TargetID != epic.ID {
		t.Fatalf("child_of edges after the move = %v, want one edge to %s", edges, epic.ID)
	}
}
