package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

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
// slug from the lowercase name.
func mustCreateScope(
	t *testing.T,
	env *TestEnv,
	typeKey string,
	name string,
	parentID uuid.UUID,
	scopeID uuid.UUID,
	actor uuid.UUID,
) *node.NodeView {
	t.Helper()
	props := map[string]json.RawMessage{
		"slug": jsonStr(lower(name)),
	}
	if typeKey == "project" {
		props["identifier"] = jsonStr(upper(name))
	}

	return mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    parentID,
			ScopeID:     scopeID,
			NodeTypeKey: typeKey,
			Name:        name,
			Props:       props,
			ActorID:     actor,
		},
	)
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

func mustFindProjectState(
	t *testing.T,
	env *TestEnv,
	projectID uuid.UUID,
	name string,
) *node.NodeView {
	t.Helper()
	nodes, err := env.Stores.Views.List(
		env.Ctx,
		node.NodeListQuery{
			OrgID:            env.OrgID,
			NodeType:         "state",
			ByTargetRelation: &node.RelationScan{NodeID: projectID, RelationType: node.RelChildOf},
		},
	)
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
