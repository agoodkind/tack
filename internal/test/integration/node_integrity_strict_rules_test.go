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

// TestNodeIntegrityStrictRejectsEveryRuleAfterActivation checks that
// NodeService.Update rejects each invalid property case after strict
// validation activates. It checks that each error wraps
// domain.ErrInvalidArgument and includes the property name, and that each
// rejected update preserves the issue's primary record and outgoing
// relationships.
func TestNodeIntegrityStrictRejectsEveryRuleAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	workspaceID := storedParentID(t, env, projectID)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	label := createSchemaNode(t, env, workspaceID, workspaceID, "label", nil)
	stateID := stateByName(t, env, projectID, "Todo")
	activateStrict(t)

	cases := []struct {
		rule     string
		property string
		value    json.RawMessage
	}{
		{rule: "undeclared property", property: "strict_test_undeclared", value: quoted("value")},
		{rule: "property that does not apply", property: "identifier", value: quoted("STRICT")},
		{rule: "wrong JSON type", property: "priority", value: json.RawMessage(`5`)},
		{rule: "option label instead of key", property: "priority", value: quoted("High")},
		{rule: "required property removal", property: "state_id", value: json.RawMessage(`null`)},
		{
			rule:     "reference to a missing node",
			property: "state_id",
			value:    quoted(uuid.New().String()),
		},
		{
			rule:     "reference to the wrong node type",
			property: "state_id",
			value:    quoted(label.ID.String()),
		},
	}
	for _, rule := range cases {
		before, _ := mutationSnapshot(t, env, issue.ID)
		_, err := env.NodeSvc.Update(
			env.Ctx,
			service.UpdateInput{
				NodeID: issue.ID,
				Name:   nil,
				Props:  schemaProps(rule.property, rule.value),
				RelationshipChanges: node.RelationshipChanges{
					Add:    nil,
					Remove: nil,
				},
				ActorID: uuid.New(),
			},
		)
		requireError(t, err, domain.ErrInvalidArgument, "the update with a "+rule.rule)
		if !strings.Contains(err.Error(), rule.property) {
			t.Fatalf(
				"the refusal of a %s = %v, want the property %s in the error",
				rule.rule,
				err,
				rule.property,
			)
		}

		requireUnchanged(t, env, before)
		after, _ := mutationSnapshot(t, env, issue.ID)
		if !reflect.DeepEqual(after.Outgoing, before.Outgoing) {
			t.Fatalf(
				"the refused update with a %s changed the relationships of issue %s",
				rule.rule,
				issue.ID,
			)
		}
	}

	before, _ := mutationSnapshot(t, env, issue.ID)
	_, err := env.NodeSvc.Update(env.Ctx, moveUpdate(issue.ID, stateID))
	requireError(t, err, domain.ErrInvalidArgument, "the parent change to a state")
	requireUnchanged(t, env, before)
	requireSingleParentEdge(t, env, issue.ID, projectID)
}
