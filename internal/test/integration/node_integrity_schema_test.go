package integration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegritySchemaRejectsUndeclaredNames creates and updates an issue
// through the service with an undeclared property and a property that does
// not apply to issues. Each request must fail with an error that includes
// the property name. The requests must not change the stored issue.
func TestNodeIntegritySchemaRejectsUndeclaredNames(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	_, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(projectID, projectID, "issue", schemaProps("progress", quoted("half"))))
	requireRefusedName(t, err, "progress")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("the refused create stored %d issues", len(issues))
	}

	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before := storedSchemaView(t, env, issue.ID)
	for _, name := range []string{"progress", "identifier"} {
		_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
			NodeID: issue.ID, Name: nil, Props: schemaProps(name, quoted("value")),
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
		})
		requireRefusedName(t, err, name)
	}
	after := storedSchemaView(t, env, issue.ID)
	if !reflect.DeepEqual(after.Props, before.Props) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("a refused update changed the stored issue: before %+v, after %+v", before, after)
	}
}

// TestNodeIntegritySchemaAppliesDefaultsAndPartialUpdates creates an issue
// without optional properties and then updates one property. Creation must
// store the declared defaults. The update must leave the other properties
// unchanged.
func TestNodeIntegritySchemaAppliesDefaultsAndPartialUpdates(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", schemaProps("description", quoted("Original description")))
	todoID := stateByName(t, env, projectID, "Todo")
	for name, want := range map[string]string{"priority": `"none"`, "is_draft": "false", "state_id": strconv.Quote(todoID.String())} {
		if got := string(issue.Props[name]); got != want {
			t.Fatalf("default %s = %s, want %s", name, got, want)
		}
	}

	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: issue.ID, Name: nil, Props: schemaProps("priority", quoted("high")),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("update priority: %v", err)
	}
	updated := storedSchemaView(t, env, issue.ID)
	if got := string(updated.Props["priority"]); got != `"high"` {
		t.Fatalf("updated priority = %s, want \"high\"", got)
	}
	for _, name := range []string{"description", "state_id", "sequence", "scope_id", "parent_id"} {
		if string(updated.Props[name]) != string(issue.Props[name]) {
			t.Fatalf("partial update changed %s from %s to %s", name, issue.Props[name], updated.Props[name])
		}
	}
}

// TestNodeIntegritySchemaIndexesScopeOfEveryType creates a project with its
// five default states, an issue, and a comment and an activity under the issue.
// It verifies that the scope_id index lists the states, comment, and activity
// under the project and the project under its workspace.
func TestNodeIntegritySchemaIndexesScopeOfEveryType(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	comment := createSchemaNode(t, env, issue.ID, projectID, "comment", schemaProps("description", quoted("A comment")))
	activity := createSchemaNode(t, env, issue.ID, projectID, "activity", nil)
	workspaceID := uuid.MustParse(strings.Trim(string(storedSchemaView(t, env, projectID).Props["scope_id"]), `"`))

	if states := listByScope(t, env, "state", projectID); len(states) != 5 {
		t.Fatalf("scope_id index lists %d states of project %s, want the 5 default states", len(states), projectID)
	}
	for _, expected := range []struct {
		typeKey string
		scopeID uuid.UUID
		nodeID  uuid.UUID
	}{
		{"comment", projectID, comment.ID},
		{"activity", projectID, activity.ID},
		{"project", workspaceID, projectID},
	} {
		listed := listByScope(t, env, expected.typeKey, expected.scopeID)
		if len(listed) != 1 || listed[0].ID != expected.nodeID {
			t.Fatalf("scope_id index lists %s nodes %v of scope %s, want node %s", expected.typeKey, listed, expected.scopeID, expected.nodeID)
		}
	}
}

func requireRefusedName(t *testing.T, err error, name string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), strconv.Quote(name)) {
		t.Fatalf("error = %v, want an invalid argument that contains %q", err, name)
	}
}

func createSchemaProject(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	workspace := createSchemaNode(t, env, env.OrgID, env.OrgID, "workspace", schemaProps("slug", quoted("main")))
	project := createSchemaNode(t, env, workspace.ID, workspace.ID, "project", map[string]json.RawMessage{
		"slug": quoted("schema"), "identifier": quoted("SCHEMA"),
	})
	return project.ID
}

func createSchemaNode(t *testing.T, env *integration.TestEnv, parentID, scopeID uuid.UUID, typeKey string, props map[string]json.RawMessage) *node.NodeView {
	t.Helper()
	created, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(parentID, scopeID, typeKey, props))
	if err != nil {
		t.Fatalf("create %s: %v", typeKey, err)
	}
	return created.View
}

func schemaCreateInput(parentID, scopeID uuid.UUID, typeKey string, props map[string]json.RawMessage) service.CreateInput {
	return service.CreateInput{
		ParentID: parentID, ScopeID: scopeID, NodeTypeKey: typeKey, Name: "Schema " + typeKey,
		Props: props, Relationships: nil, ActorID: uuid.New(),
		IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	}
}

func storedSchemaView(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) *node.NodeView {
	t.Helper()
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("get node %s: view %v, err %v", nodeID, view, err)
	}
	return view
}

func listByScope(t *testing.T, env *integration.TestEnv, typeKey string, scopeID uuid.UUID) []*node.Node {
	t.Helper()
	nodes, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, typeKey, "scope_id", quoted(scopeID.String()))
	if err != nil {
		t.Fatalf("list %s nodes by scope %s: %v", typeKey, scopeID, err)
	}
	return nodes
}

func stateByName(t *testing.T, env *integration.TestEnv, projectID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	states, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", quoted(projectID.String()))
	if err != nil {
		t.Fatalf("list states of project %s: %v", projectID, err)
	}
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	t.Fatalf("project %s has no state %q", projectID, name)
	return uuid.Nil
}
