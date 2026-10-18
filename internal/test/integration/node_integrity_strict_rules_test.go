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
		{rule: "reference to a missing node", property: "state_id", value: quoted(uuid.New().String())},
		{rule: "reference to the wrong node type", property: "state_id", value: quoted(label.ID.String())},
		{rule: "parent of a disallowed type", property: "parent_id", value: quoted(stateID.String())},
	}
	for _, rule := range cases {
		before, _ := mutationSnapshot(t, env, issue.ID)
		_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
			NodeID: issue.ID, Name: nil, Props: schemaProps(rule.property, rule.value),
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
		})
		requireError(t, err, domain.ErrInvalidArgument, "the update with a "+rule.rule)
		if !strings.Contains(err.Error(), rule.property) {
			t.Fatalf("the refusal of a %s = %v, want the property %s in the error", rule.rule, err, rule.property)
		}
		requireUnchanged(t, env, before)
		after, _ := mutationSnapshot(t, env, issue.ID)
		if !reflect.DeepEqual(after.Outgoing, before.Outgoing) {
			t.Fatalf("the refused update with a %s changed the relationships of issue %s", rule.rule, issue.ID)
		}
	}
}

// TestNodeIntegrityStrictRejectsInvalidCreateAndEdgeAfterActivation checks
// that strict validation rejects an issue creation under a label through
// NodeService.Create and rejects RelationshipStore.Add calls for a child_of
// edge and an edge to the organization node of another organization. It
// checks that each error wraps domain.ErrInvalidArgument and that the project
// still lists only the existing issue. It also checks that the issue retains
// exactly one child_of edge to the project and does not have any labeled_with
// edges.
func TestNodeIntegrityStrictRejectsInvalidCreateAndEdgeAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	workspaceID := storedParentID(t, env, projectID)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	label := createSchemaNode(t, env, workspaceID, workspaceID, "label", nil)
	otherOrgID := createStrictOtherOrg(t, env)
	activateStrict(t)

	_, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(label.ID, projectID, "issue", nil))
	requireError(t, err, domain.ErrInvalidArgument, "the create of an issue under a label")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 1 {
		t.Fatalf("project %s lists %d issues after the refused create, want 1", projectID, len(issues))
	}
	for name, relationship := range map[string]*node.Relationship{
		"a child_of edge":                 {OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelChildOf, TargetID: workspaceID, CreatedBy: uuid.New()},
		"an edge to another organization": {OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelLabeledWith, TargetID: otherOrgID, CreatedBy: uuid.New()},
	} {
		requireError(t, env.Stores.Relationships.Add(env.Ctx, relationship), domain.ErrInvalidArgument, "the store add of "+name)
	}
	parents, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(parents) != 1 || parents[0].TargetID != projectID {
		t.Fatalf("child_of edges of issue %s after the refused adds = %v, %v, want one edge to %s", issue.ID, parents, err, projectID)
	}
	labels, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelLabeledWith)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labeled_with edges of issue %s after the refused add = %v, %v, want none", issue.ID, labels, err)
	}
}

// TestNodeIntegrityStrictScanRefusesDisagreeingParentEdge adds a second
// child_of edge to an issue through RelationshipStore.Add before activation.
// The command ops verify strict-validation must report the issue as the only
// violation with its parent_id reason and must leave strict validation
// inactive.
func TestNodeIntegrityStrictScanRefusesDisagreeingParentEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	extra := &node.Relationship{OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelChildOf, TargetID: storedParentID(t, env, projectID), CreatedBy: uuid.New()}
	if err := env.Stores.Relationships.Add(env.Ctx, extra); err != nil {
		t.Fatalf("add a second child_of edge to issue %s: %v", issue.ID, err)
	}

	output, err := runStrictValidation(t, true)
	violations := output.Result.FirstViolations
	if err == nil || output.Result.Activated || len(violations) != 1 || violations[0].NodeID != issue.ID || !strings.Contains(violations[0].Reason, "parent_id") {
		t.Fatalf("strict validation = %+v, %v; want one parent_id violation for issue %s", output.Result, err, issue.ID)
	}
	stored, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || stored.Active {
		t.Fatalf("activation after the refused scan = %+v, %v; want inactive", stored, err)
	}
}

// activateStrict runs ops verify strict-validation with --execute and
// requires activation.
func activateStrict(t *testing.T) {
	t.Helper()
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated {
		t.Fatalf("strict validation = %+v, %v; want activation", output.Result, err)
	}
}

func storedParentID(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) uuid.UUID {
	t.Helper()
	snapshot, _ := mutationSnapshot(t, env, nodeID)
	if snapshot.ParentID == uuid.Nil {
		t.Fatalf("node %s does not store a parent_id", nodeID)
	}
	return snapshot.ParentID
}
