package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// TestCreateProjectThenResolve creates a workspace and a project, then
// resolves the project by its identifier through the same code path the MCP
// resolver uses. The yesterday bug shipped because the seed missed the
// indexed "identifier" PropertyDef; the resolver's ListByProperty call
// returned zero candidates and projects were unfindable.
//
// Removing the identifier PropertyDef from seed will fail this test.
func TestCreateProjectThenResolve(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()

	ws := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    env.OrgID,
			ScopeID:     env.OrgID,
			NodeTypeKey: "workspace",
			Name:        "Main",
			Props: map[string]json.RawMessage{
				"slug": jsonStr("main"),
			},
			ActorID: actor,
		},
	)

	proj := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    ws.ID,
			ScopeID:     ws.ID,
			NodeTypeKey: "project",
			Name:        "Tack",
			Props: map[string]json.RawMessage{
				"slug":       jsonStr("tack"),
				"identifier": jsonStr("TACK"),
			},
			ActorID: actor,
		},
	)

	identRaw, _ := json.Marshal("TACK")
	candidates, err := env.Stores.Nodes.ListByProperty(
		env.Ctx,
		env.OrgID,
		"project",
		"identifier",
		identRaw,
	)
	if err != nil {
		t.Fatalf("ListByProperty: %v", err)
	}

	if len(candidates) == 0 {
		t.Fatal(
			`resolver lookup returned zero candidates for identifier="TACK"; the indexed PropertyDef is missing or not being written`,
		)
	}

	found := false

	for _, c := range candidates {
		if c.ID == proj.ID {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("resolver did not return the just-created project; candidates=%v", candidates)
	}
}
