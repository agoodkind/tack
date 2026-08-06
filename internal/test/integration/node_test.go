package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
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

	ws := mustCreate(t, env, service.CreateInput{
		ParentID:    env.OrgID,
		ScopeID:     env.OrgID,
		NodeTypeKey: "workspace",
		Name:        "Main",
		Props: map[string]json.RawMessage{
			"slug": jsonStr("main"),
		},
		ActorID: actor,
	})

	proj := mustCreate(t, env, service.CreateInput{
		ParentID:    ws.ID,
		ScopeID:     ws.ID,
		NodeTypeKey: "project",
		Name:        "Tack",
		Props: map[string]json.RawMessage{
			"slug":       jsonStr("tack"),
			"identifier": jsonStr("TACK"),
		},
		ActorID: actor,
	})

	identRaw, _ := json.Marshal("TACK")
	candidates, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "project", "identifier", identRaw)
	if err != nil {
		t.Fatalf("ListByProperty: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal(`resolver lookup returned zero candidates for identifier="TACK"; the indexed PropertyDef is missing or not being written`)
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

	epicA := mustCreate(t, env, service.CreateInput{
		ParentID:    proj.ID,
		ScopeID:     proj.ID,
		NodeTypeKey: "epic",
		Name:        "Epic A",
		ActorID:     actor,
	})
	epicB := mustCreate(t, env, service.CreateInput{
		ParentID:    proj.ID,
		ScopeID:     proj.ID,
		NodeTypeKey: "epic",
		Name:        "Epic B",
		ActorID:     actor,
	})

	// Issue under epic A. ParentID is the epic, ScopeID is the project.
	issue1 := mustCreate(t, env, service.CreateInput{
		ParentID:    epicA.ID,
		ScopeID:     proj.ID,
		NodeTypeKey: "issue",
		Name:        "First",
		ActorID:     actor,
	})
	// Issue under epic B in the same project.
	issue2 := mustCreate(t, env, service.CreateInput{
		ParentID:    epicB.ID,
		ScopeID:     proj.ID,
		NodeTypeKey: "issue",
		Name:        "Second",
		ActorID:     actor,
	})

	if got := stringPropOf(issue1, "parent_id"); got != epicA.ID.String() {
		t.Errorf("issue1 parent_id: got %q want %q", got, epicA.ID.String())
	}
	if got := stringPropOf(issue1, "scope_id"); got != proj.ID.String() {
		t.Errorf("issue1 scope_id: got %q want %q", got, proj.ID.String())
	}

	seq1 := numberPropOf(t, issue1, "sequence")
	seq2 := numberPropOf(t, issue2, "sequence")
	if seq1 != 3 || seq2 != 4 {
		t.Errorf("sequences should share the project numbering space, got issue1=%d issue2=%d (want 3 and 4)", seq1, seq2)
	}
}

func TestCreateIssueDefaultsStateIDToProjectTodo(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Tack", workspace.ID, workspace.ID, actor)
	todoState := mustFindProjectState(t, env, project.ID, "Todo")

	issue := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Defaulted",
		ActorID:     actor,
	})

	if got := uuidPropOf(t, issue, "state_id"); got != todoState.ID {
		t.Fatalf("state_id = %s, want project-local Todo state %s", got, todoState.ID)
	}
}

func TestCreateIssuePreservesExplicitStateID(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Tack", workspace.ID, workspace.ID, actor)
	doneState := mustFindProjectState(t, env, project.ID, "Done")

	issue := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Explicit",
		Props: map[string]json.RawMessage{
			"state_id": jsonStr(doneState.ID.String()),
		},
		ActorID: actor,
	})

	if got := uuidPropOf(t, issue, "state_id"); got != doneState.ID {
		t.Fatalf("state_id = %s, want explicit state %s", got, doneState.ID)
	}
}

// TestRelationshipReverseIndex verifies the relationship store maintains the
// forward and reverse indexes in sync. Adding a relationship makes it visible
// from both source and target sides; removing it clears both directions.
func TestRelationshipReverseIndex(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)
	issue := mustCreate(t, env, service.CreateInput{
		ParentID: proj.ID, ScopeID: proj.ID, NodeTypeKey: "issue", Name: "I", ActorID: actor,
	})
	target := mustCreate(t, env, service.CreateInput{
		ParentID: proj.ID, ScopeID: proj.ID, NodeTypeKey: "issue", Name: "T", ActorID: actor,
	})

	rel := &node.Relationship{
		OrgID:        env.OrgID,
		SourceID:     issue.ID,
		RelationType: "blocked_by",
		TargetID:     target.ID,
		CreatedBy:    actor,
	}
	if err := env.NodeSvc.AddRelationship(env.Ctx, rel); err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}

	// Forward: blocked_by from issue.
	out, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListBySource: %v", err)
	}
	if len(out) != 1 || out[0].TargetID != target.ID {
		t.Fatalf("forward index missing or wrong: %+v", out)
	}
	// Reverse: blocked_by pointing at target.
	in, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, target.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListByTarget: %v", err)
	}
	if len(in) != 1 || in[0].SourceID != issue.ID {
		t.Fatalf("reverse index missing or wrong: %+v", in)
	}

	if err := env.NodeSvc.RemoveRelationship(env.Ctx, env.OrgID, issue.ID, "blocked_by", target.ID); err != nil {
		t.Fatalf("RemoveRelationship: %v", err)
	}
	out, _ = env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "blocked_by")
	in, _ = env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, target.ID, "blocked_by")
	if len(out) != 0 || len(in) != 0 {
		t.Fatalf("relationship not fully cleared: forward=%v reverse=%v", out, in)
	}
}

// TestUpdatePropFiltersStillFind covers UpdateAtomic's secondary index
// reconciliation. Updating an indexed property must clear the old index
// entry; otherwise a stale entry leaves the old value findable forever.
func TestUpdatePropFiltersStillFind(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)

	issue := mustCreate(t, env, service.CreateInput{
		ParentID:    proj.ID,
		ScopeID:     proj.ID,
		NodeTypeKey: "issue",
		Name:        "Movable",
		Props: map[string]json.RawMessage{
			"priority": jsonStr("high"),
		},
		ActorID: actor,
	})

	highRaw, _ := json.Marshal("high")
	hits, _ := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", highRaw)
	if !containsID(hits, issue.ID) {
		t.Fatal(`expected to find issue under priority="high" before update`)
	}

	newPriority := jsonStr("low")
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID:  issue.ID,
		Props:   map[string]json.RawMessage{"priority": newPriority},
		ActorID: actor,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	hits, _ = env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", highRaw)
	if containsID(hits, issue.ID) {
		t.Fatal(`stale index entry: issue still findable under priority="high" after update to "low"`)
	}
	lowRaw, _ := json.Marshal("low")
	hits, _ = env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "issue", "priority", lowRaw)
	if !containsID(hits, issue.ID) {
		t.Fatal(`expected to find issue under priority="low" after update`)
	}
}

// TestDeleteClearsRelationships ensures deleting a node also clears every
// relationship it participates in, in both directions. Without this, a node
// can be gone but still referenced by a "blocks" or "assigned_to" edge,
// leading to dangling reads from the resolver.
func TestDeleteClearsRelationships(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)
	issue := mustCreate(t, env, service.CreateInput{
		ParentID: proj.ID, ScopeID: proj.ID, NodeTypeKey: "issue", Name: "I", ActorID: actor,
	})
	other := mustCreate(t, env, service.CreateInput{
		ParentID: proj.ID, ScopeID: proj.ID, NodeTypeKey: "issue", Name: "O", ActorID: actor,
	})

	if err := env.NodeSvc.AddRelationship(env.Ctx, &node.Relationship{
		OrgID: env.OrgID, SourceID: issue.ID, RelationType: "blocks", TargetID: other.ID, CreatedBy: actor,
	}); err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}

	if err := env.NodeSvc.Delete(env.Ctx, issue.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	out, _ := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "")
	in, _ := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, other.ID, "")
	if len(out) != 0 {
		t.Errorf("forward edges from deleted node should be gone: %v", out)
	}
	if len(in) != 0 {
		t.Errorf("reverse edges to surviving node should be gone: %v", in)
	}
}

// --- helpers ---

func mustCreate(t *testing.T, env *TestEnv, in service.CreateInput) *node.NodeView {
	t.Helper()
	res, err := env.NodeSvc.Create(env.Ctx, in)
	if err != nil {
		t.Fatalf("create %s %q: %v", in.NodeTypeKey, in.Name, err)
	}
	if res == nil || res.View == nil {
		t.Fatalf("create %s %q: nil view", in.NodeTypeKey, in.Name)
	}
	return res.View
}

// mustCreateScope is a thin wrapper for the scaffolding pattern (workspace
// then project) that nearly every test uses. It seeds the props map with the
// slug derived from the lowercase name so the slug index gets populated.
func mustCreateScope(t *testing.T, env *TestEnv, typeKey, name string, parentID, scopeID, actor uuid.UUID) *node.NodeView {
	t.Helper()
	props := map[string]json.RawMessage{
		"slug": jsonStr(lower(name)),
	}
	if typeKey == "project" {
		props["identifier"] = jsonStr(upper(name))
	}
	return mustCreate(t, env, service.CreateInput{
		ParentID:    parentID,
		ScopeID:     scopeID,
		NodeTypeKey: typeKey,
		Name:        name,
		Props:       props,
		ActorID:     actor,
	})
}

func jsonStr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func stringPropOf(v *node.NodeView, key string) string {
	if v == nil {
		return ""
	}
	raw, ok := v.Props[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func numberPropOf(t *testing.T, v *node.NodeView, key string) int64 {
	t.Helper()
	if v == nil {
		return 0
	}
	raw, ok := v.Props[key]
	if !ok {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("decode prop %q as int: %v (raw=%s)", key, err, string(raw))
	}
	return n
}

func uuidPropOf(t *testing.T, v *node.NodeView, key string) uuid.UUID {
	t.Helper()
	if v == nil {
		return uuid.Nil
	}
	raw, ok := v.Props[key]
	if !ok {
		return uuid.Nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode prop %q as UUID string: %v (raw=%s)", key, err, string(raw))
	}
	id, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("parse prop %q as UUID: %v (value=%q)", key, err, value)
	}
	return id
}

func mustFindProjectState(t *testing.T, env *TestEnv, projectID uuid.UUID, name string) *node.Node {
	t.Helper()
	nodes, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", jsonStr(projectID.String()))
	if err != nil {
		t.Fatalf("list states for project %s: %v", projectID, err)
	}
	for _, state := range nodes {
		if state.Name == name {
			return state
		}
	}
	t.Fatalf("project %s state %q not found", projectID, name)
	return nil
}

func containsID(nodes []*node.Node, id uuid.UUID) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func lower(s string) string { return toCase(s, false) }
func upper(s string) string { return toCase(s, true) }
func toCase(s string, up bool) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case up && c >= 'a' && c <= 'z':
			c -= 32
		case !up && c >= 'A' && c <= 'Z':
			c += 32
		}
		out[i] = c
	}
	return string(out)
}
