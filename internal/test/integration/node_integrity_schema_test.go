package integration_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
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
	_, err := env.NodeSvc.Create(
		env.Ctx,
		schemaCreateInput(projectID, projectID, "issue", schemaProps("progress", quoted("half"))),
	)
	requireRefusedName(t, err, "progress")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("the refused create stored %d issues", len(issues))
	}

	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	before := storedStoreView(t, env.Stores, issue.ID)

	for _, name := range []string{"progress", "identifier"} {
		_, err := env.NodeSvc.Update(
			env.Ctx,
			service.UpdateInput{
				NodeID: issue.ID,
				Name:   nil,
				Props:  schemaProps(name, quoted("value")),
				RelationshipChanges: node.RelationshipChanges{
					Add:    nil,
					Remove: nil,
				},
				ActorID: uuid.New(),
			},
		)
		requireRefusedName(t, err, name)
	}

	after := storedStoreView(t, env.Stores, issue.ID)
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
	issue := createSchemaNode(
		t,
		env,
		projectID,
		projectID,
		"issue",
		schemaProps("description", quoted("Original description")),
	)

	todoID := stateByName(t, env, projectID, "Todo")
	for name, want := range map[string]string{
		"priority": `"none"`,
		"is_draft": "false",
		"state_id": strconv.Quote(todoID.String()),
	} {
		if got := string(issue.Props[name]); got != want {
			t.Fatalf("default %s = %s, want %s", name, got, want)
		}
	}

	if _, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID:              issue.ID,
			Name:                nil,
			Props:               schemaProps("priority", quoted("high")),
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
			ActorID:             uuid.New(),
		},
	); err != nil {
		t.Fatalf("update priority: %v", err)
	}

	updated := storedStoreView(t, env.Stores, issue.ID)
	if got := string(updated.Props["priority"]); got != `"high"` {
		t.Fatalf("updated priority = %s, want \"high\"", got)
	}

	for _, name := range []string{"description", "state_id", "sequence", "scope_id"} {
		if string(updated.Props[name]) != string(issue.Props[name]) {
			t.Fatalf(
				"partial update changed %s from %s to %s",
				name,
				issue.Props[name],
				updated.Props[name],
			)
		}
	}

	requireStoredParent(t, env, issue.ID, projectID)
}

// TestNodeIntegritySchemaIndexesScopeOfEveryType creates a project with its
// five default states, an issue, and a comment and an activity under the issue.
// It verifies that the `scope_id` index lists the states, issue, comment, and
// activity under the project and lists the project under its workspace.
func TestNodeIntegritySchemaIndexesScopeOfEveryType(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	comment := createSchemaNode(
		t,
		env,
		issue.ID,
		projectID,
		"comment",
		schemaProps("description", quoted("A comment")),
	)
	activity := createSchemaNode(t, env, issue.ID, projectID, "activity", nil)
	workspaceID := uuid.MustParse(
		strings.Trim(string(storedStoreView(t, env.Stores, projectID).Props["scope_id"]), `"`),
	)

	if states := listByScope(t, env, "state", projectID); len(states) != 5 {
		t.Fatalf(
			"scope_id index lists %d states of project %s, want the 5 default states",
			len(states),
			projectID,
		)
	}

	for _, expected := range []struct {
		typeKey string
		scopeID uuid.UUID
		nodeID  uuid.UUID
	}{
		{"issue", projectID, issue.ID},
		{"comment", projectID, comment.ID},
		{"activity", projectID, activity.ID},
		{"project", workspaceID, projectID},
	} {
		listed := listByScope(t, env, expected.typeKey, expected.scopeID)
		if len(listed) != 1 || listed[0].ID != expected.nodeID {
			t.Fatalf(
				"scope_id index lists %s nodes %v of scope %s, want node %s",
				expected.typeKey,
				listed,
				expected.scopeID,
				expected.nodeID,
			)
		}
	}
}
