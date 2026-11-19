package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditRelationshipReplacement replaces a stored relationship,
// moves an issue to another parent, and removes the relationship. Each
// change event must record the target identity and the actual old and new
// values that the transaction read and wrote.
func TestRuntimeAuditRelationshipReplacement(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	issue := createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	workspaceID := storedParentID(t, env, projectID)
	label := createSchemaNode(t, env, workspaceID, workspaceID, "label", nil)
	if err := env.NodeSvc.AddRelationship(
		env.Ctx,
		labelEdge(env, issue.ID, label.ID, uuid.New()),
	); err != nil {
		t.Fatalf("add the first edge: %v", err)
	}

	stored := storedEdge(t, env, issue.ID, node.RelLabeledWith, label.ID)
	mark := outboxTail(t, env)
	replaceContext := integration.WithTestOperation(t, env.Ctx)
	if err := env.NodeSvc.AddRelationship(
		replaceContext,
		labelEdge(env, issue.ID, label.ID, uuid.New()),
	); err != nil {
		t.Fatalf("replace the edge: %v", err)
	}

	replacement := storedEdge(t, env, issue.ID, node.RelLabeledWith, label.ID)
	update := requireOneEvent(
		t,
		outboxEventsAfter(t, env, mark),
		audit.VerbRelationshipUpdate,
		issue.ID,
	)
	requireChangeEvent(t, update, operationIDOf(t, replaceContext), integration.TestActorID)
	requireRelationshipEvent(t, update, issue.ID, node.RelLabeledWith, label.ID)
	requireJSONEqual(t, "replaced old value", update.Delta.Before, stored)
	requireJSONEqual(t, "replaced new value", update.Delta.After, replacement)

	epicParent := storedEdge(t, env, issue.ID, node.RelChildOf, epic.ID)
	mark = outboxTail(t, env)
	moveContext := integration.WithTestOperation(t, env.Ctx)
	if _, err := env.NodeSvc.Update(
		moveContext,
		service.UpdateInput{
			NodeID: issue.ID,
			Name:   nil,
			Props:  nil,
			RelationshipChanges: node.RelationshipChanges{
				Add: []*node.Relationship{
					{
						OrgID:        env.OrgID,
						SourceID:     issue.ID,
						RelationType: node.RelChildOf,
						TargetID:     projectID,
					},
				},
				Remove: nil,
			},
			ActorID: integration.TestActorID,
		},
	); err != nil {
		t.Fatalf("move issue %s: %v", issue.ID, err)
	}

	moved := outboxEventsAfter(t, env, mark)
	for _, event := range moved {
		requireChangeEvent(t, event, operationIDOf(t, moveContext), integration.TestActorID)
	}

	requireOneEvent(t, moved, audit.VerbNodeUpdate, issue.ID)
	removedParent := requireOneEvent(t, moved, audit.VerbRelationshipRemove, issue.ID)
	addedParent := requireOneEvent(t, moved, audit.VerbRelationshipAdd, issue.ID)
	requireRelationshipEvent(t, removedParent, issue.ID, node.RelChildOf, epic.ID)
	requireRelationshipEvent(t, addedParent, issue.ID, node.RelChildOf, projectID)
	projectParent := storedEdge(t, env, issue.ID, node.RelChildOf, projectID)
	requireJSONEqual(t, "removed parent old value", removedParent.Delta.Before, epicParent)
	requireJSONEqual(t, "added parent new value", addedParent.Delta.After, projectParent)
	if len(removedParent.Delta.After) != 0 || len(addedParent.Delta.Before) != 0 {
		t.Fatalf(
			"removed parent new value = %s and added parent old value = %s, want none",
			removedParent.Delta.After,
			addedParent.Delta.Before,
		)
	}

	if removedParent.Context.OperationID != addedParent.Context.OperationID {
		t.Fatalf(
			"parent change operations = %s and %s, want one operation",
			removedParent.Context.OperationID,
			addedParent.Context.OperationID,
		)
	}

	mark = outboxTail(t, env)
	if err := env.NodeSvc.RemoveRelationship(
		env.Ctx,
		env.OrgID,
		issue.ID,
		node.RelLabeledWith,
		label.ID,
	); err != nil {
		t.Fatalf("remove the edge: %v", err)
	}

	removed := requireOneEvent(
		t,
		outboxEventsAfter(t, env, mark),
		audit.VerbRelationshipRemove,
		issue.ID,
	)
	requireJSONEqual(t, "removed old value", removed.Delta.Before, replacement)
	if len(removed.Delta.After) != 0 {
		t.Fatalf("removed edge new value = %s, want none", removed.Delta.After)
	}
}
