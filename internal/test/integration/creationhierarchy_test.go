package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// TestCreateEpicAndIssueInProject covers the scope_id / parent_id split
// from TACK-164. An issue created under an epic must:
//   - be parented to the epic via Props["parent_id"]
//   - be scoped to the project via Props["scope_id"]
//   - allocate its sequence under the project, not under the epic
//
// Every work-item type in the project shares one numbering space. The epics
// take 1 and 2, so the issues must receive 3 and 4.
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

	if got := stringPropOf(issue1, "parent_id"); got != epicA.ID.String() {
		t.Errorf("issue1 parent_id: got %q want %q", got, epicA.ID.String())
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
