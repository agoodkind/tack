package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestCreateEpicAndIssueInProject covers the scope and parent relationship
// from TACK-164. An issue created under an epic must:
//   - have one child_of edge to the epic
//   - be scoped to the project via Props["scope_id"]
//   - allocate its sequence under the project, not under the epic
//
// Every work-item type in the project shares one numbering space. The epics
// receive 1 and 2. The issues must receive 3 and 4.
func TestCreateEpicAndIssueInProject(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)

	epicA := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "epic",
			Name:        "Epic A",
			ActorID:     actor,
		},
	)
	epicB := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "epic",
			Name:        "Epic B",
			ActorID:     actor,
		},
	)

	// Issue under epic A. ParentID is the epic, ScopeID is the project.
	issue1 := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    epicA.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "First",
			ActorID:     actor,
		},
	)
	// Issue under epic B in the same project.
	issue2 := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    epicB.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "Second",
			ActorID:     actor,
		},
	)

	parents, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issue1.ID,
		node.RelChildOf,
	)
	if err != nil || len(parents) != 1 || parents[0].TargetID != epicA.ID {
		t.Errorf("issue1 parent edges = %v, %v, want %s", parents, err, epicA.ID)
	}

	if _, exists := issue1.Props["parent_id"]; exists {
		t.Errorf("issue %s stores parent_id", issue1.ID)
	}

	if got := stringPropOf(issue1, "scope_id"); got != proj.ID.String() {
		t.Errorf("issue1 scope_id: got %q want %q", got, proj.ID.String())
	}

	seq1 := numberPropOf(t, issue1, "sequence")
	seq2 := numberPropOf(t, issue2, "sequence")
	if seq1 != 3 || seq2 != 4 {
		t.Errorf(
			"sequences should share the project numbering space, got issue1=%d issue2=%d (want 3 and 4)",
			seq1,
			seq2,
		)
	}
}

func TestCreateIssuePreservesExplicitStateID(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Tack", workspace.ID, workspace.ID, actor)
	doneState := mustFindProjectState(t, env, project.ID, "Done")

	issue := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    project.ID,
			ScopeID:     project.ID,
			NodeTypeKey: "issue",
			Name:        "Explicit",
			Props: map[string]json.RawMessage{
				"state_id": jsonStr(doneState.ID.String()),
			},
			ActorID: actor,
		},
	)

	if got := uuidPropOf(t, issue, "state_id"); got != doneState.ID {
		t.Fatalf("state_id = %s, want explicit state %s", got, doneState.ID)
	}
}
