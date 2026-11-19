package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// TestUpdatePropFiltersStillFind covers UpdateAtomic's secondary index
// reconciliation. Updating an indexed property must clear the old index
// entry; otherwise a stale entry leaves the old value findable forever.
func TestUpdatePropFiltersStillFind(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)

	issue := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "Movable",
			Props: map[string]json.RawMessage{
				"priority": jsonStr("high"),
			},
			ActorID: actor,
		},
	)

	highRaw, _ := json.Marshal("high")
	hits, _ := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", highRaw)
	if !containsID(hits, issue.ID) {
		t.Fatal(`expected to find issue under priority="high" before update`)
	}

	newPriority := jsonStr("low")
	if _, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID:  issue.ID,
			Props:   map[string]json.RawMessage{"priority": newPriority},
			ActorID: actor,
		},
	); err != nil {
		t.Fatalf("Update: %v", err)
	}

	hits, _ = env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", highRaw)
	if containsID(hits, issue.ID) {
		t.Fatal(
			`stale index entry: issue still findable under priority="high" after update to "low"`,
		)
	}

	lowRaw, _ := json.Marshal("low")
	hits, _ = env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", lowRaw)
	if !containsID(hits, issue.ID) {
		t.Fatal(`expected to find issue under priority="low" after update`)
	}
}
