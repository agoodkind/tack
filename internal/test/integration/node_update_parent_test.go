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

// TestNodeServiceUpdateRefusesParentProperty checks that the service rejects
// undeclared parent_id values and preserves the child_of edge.
func TestNodeServiceUpdateRefusesParentProperty(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	issueID := createTestIssue(t, env, projectID, "text parent refused")

	for _, value := range []json.RawMessage{
		json.RawMessage(strconv.Quote(projectID.String())),
		json.RawMessage("null"),
	} {
		_, err := env.NodeSvc.Update(
			env.Ctx,
			service.UpdateInput{
				NodeID: issueID,
				Name:   nil,
				Props: map[string]json.RawMessage{
					"parent_id": value,
				},
				RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
				ActorID:             uuid.New(),
			},
		)

		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("Update with parent_id %s returned %v, want invalid argument", value, err)
		}
	}

	edges, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issueID,
		node.RelChildOf,
	)
	if err != nil || len(edges) != 1 || edges[0].TargetID != projectID {
		t.Fatalf("parent edges after the refused update = %v, %v, want %s", edges, err, projectID)
	}
}

// TestNodeServiceUpdateMovesChildOfEdge checks that a valid parent change
// moves the child_of edge in the same write.
func TestNodeServiceUpdateMovesChildOfEdge(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	issueID := createTestIssue(t, env, projectID, "moved issue")
	epic := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    projectID,
			ScopeID:     projectID,
			NodeTypeKey: "epic",
			Name:        "target epic",
			ActorID:     uuid.New(),
		},
	)

	if _, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID: issueID,
			Name:   nil,
			Props:  nil,
			RelationshipChanges: node.RelationshipChanges{
				Add: []*node.Relationship{
					{SourceID: issueID, RelationType: node.RelChildOf, TargetID: epic.ID},
				},
			},
			ActorID: uuid.New(),
		},
	); err != nil {
		t.Fatalf("Update with the epic as parent: %v", err)
	}

	edges, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issueID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("list child_of edges: %v", err)
	}

	if len(edges) != 1 || edges[0].TargetID != epic.ID {
		t.Fatalf("child_of edges after the move = %v, want one edge to %s", edges, epic.ID)
	}
}
