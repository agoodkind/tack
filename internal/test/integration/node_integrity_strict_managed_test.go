package integration_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// strictManagedSequence is a sequence number that the issue's project counter
// did not issue.
const strictManagedSequence = "999"

// TestNodeIntegrityStrictRejectsClientManagedValuesAfterActivation activates
// strict validation and then calls NodeService.Update with a client value for
// each property that node.IsManagedProperty reports and that a client cannot
// set through a move: the issue's scope_id, set to another project, and its
// sequence. Each update must fail with an error that wraps
// domain.ErrInvalidArgument. Each refused update must leave the issue's
// primary record and outgoing relationships unchanged.
func TestNodeIntegrityStrictRejectsClientManagedValuesAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	workspaceID := storedParentID(t, env, projectID)
	otherProject := createSchemaNode(t, env, workspaceID, workspaceID, "project", map[string]json.RawMessage{
		"slug": quoted("other"), "identifier": quoted("OTHER"),
	})
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	activateStrict(t)

	cases := []struct {
		property string
		value    json.RawMessage
	}{
		{property: "scope_id", value: quoted(otherProject.ID.String())},
		{property: "sequence", value: json.RawMessage(strictManagedSequence)},
	}
	for _, managed := range cases {
		t.Run(managed.property, func(t *testing.T) {
			if !node.IsManagedProperty(managed.property) {
				t.Fatalf("node.IsManagedProperty(%q) = false, want a service-managed property", managed.property)
			}
			before, _ := mutationSnapshot(t, env, issue.ID)
			view, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
				NodeID: issue.ID, Name: nil, Props: schemaProps(managed.property, managed.value),
				RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
			})
			if view != nil {
				t.Errorf("the update returned the view with %s = %s", managed.property, view.Props[managed.property])
			}
			requireError(t, err, domain.ErrInvalidArgument, "the client update of the service-managed property "+managed.property)
			requireUnchanged(t, env, before)
			after, _ := mutationSnapshot(t, env, issue.ID)
			if !reflect.DeepEqual(after.Outgoing, before.Outgoing) {
				t.Fatalf("the refused update of %s changed the relationships of issue %s", managed.property, issue.ID)
			}
		})
	}
}
