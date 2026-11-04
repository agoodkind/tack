package integration_test

import (
	"encoding/json"
	"reflect"
	"strings"
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
// strict validation and then sends a client value for each property that
// node.IsManagedProperty reports and that a client cannot set through a move:
// scope_id, set to another project, and sequence. NodeService.Update and
// NodeService.Create must each fail with an error that wraps
// domain.ErrInvalidArgument and contains the property name. Each refused
// update must leave the issue's primary record and outgoing relationships
// unchanged, and each refused create must not store an issue.
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
			_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
				NodeID: issue.ID, Name: nil, Props: schemaProps(managed.property, managed.value),
				RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
			})
			requireManagedRefusal(t, err, "the client update", managed.property)
			requireUnchanged(t, env, before)
			after, _ := mutationSnapshot(t, env, issue.ID)
			if !reflect.DeepEqual(after.Outgoing, before.Outgoing) {
				t.Fatalf("the refused update of %s changed the relationships of issue %s", managed.property, issue.ID)
			}

			_, err = env.NodeSvc.Create(env.Ctx, schemaCreateInput(projectID, projectID, "issue", schemaProps(managed.property, managed.value)))
			requireManagedRefusal(t, err, "the client create", managed.property)
			if issues := listByScope(t, env, "issue", projectID); len(issues) != 1 {
				t.Fatalf("project %s lists %d issues after the refused create, want 1", projectID, len(issues))
			}
		})
	}
}

// requireManagedRefusal fails unless err wraps domain.ErrInvalidArgument and
// contains property.
func requireManagedRefusal(t *testing.T, err error, operation, property string) {
	t.Helper()
	requireError(t, err, domain.ErrInvalidArgument, operation+" of the service-managed property "+property)
	if !strings.Contains(err.Error(), property) {
		t.Fatalf("%s of %s = %v, want the property in the error", operation, property, err)
	}
}
