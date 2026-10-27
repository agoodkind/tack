package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditMutationMissingContext writes through the node service
// without an audit operation and with an operation that fails event
// validation inside the commit transaction. Each write must return an error
// and leave the node and its relationships unchanged. The writes must not add
// any outbox events.
func TestRuntimeAuditMutationMissingContext(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	label := createSchemaNode(t, env, projectID, projectID, "label", nil)
	mark := outboxTail(t, env)
	renamed := "Renamed"
	edge := labelEdge(env, issue.ID, label.ID, uuid.New())
	rename := service.UpdateInput{
		NodeID: issue.ID, Name: &renamed, Props: nil,
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}

	missing := context.Background()
	if _, err := env.NodeSvc.Update(missing, rename); !errors.Is(err, audit.ErrMissingMutationContext) {
		t.Fatalf("update without an operation = %v, want audit.ErrMissingMutationContext", err)
	}
	if err := env.NodeSvc.AddRelationship(missing, edge); !errors.Is(err, audit.ErrMissingMutationContext) {
		t.Fatalf("add relationship without an operation = %v, want audit.ErrMissingMutationContext", err)
	}

	failing, err := audit.WithMutationContext(env.Ctx, futureOperation(t))
	if err != nil {
		t.Fatalf("attach the failing operation: %v", err)
	}
	if _, err := env.NodeSvc.Update(failing, rename); err == nil {
		t.Fatal("an update with an invalid change event must fail")
	}
	if err := env.NodeSvc.AddRelationship(failing, edge); err == nil {
		t.Fatal("a relationship add with an invalid change event must fail")
	}
	if _, err := env.NodeSvc.Delete(failing, issue.ID, uuid.New()); err == nil {
		t.Fatal("a delete with an invalid change event must fail")
	}

	view, err := env.Stores.Views.Get(env.Ctx, issue.ID)
	if err != nil || view == nil || view.Name != issue.Name {
		t.Fatalf("issue %s after the refused writes = %+v, %v, want name %q", issue.ID, view, err, issue.Name)
	}
	labels, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelLabeledWith)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labeled_with edges of issue %s = %+v, %v, want none", issue.ID, labels, err)
	}
	parents, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(parents) != 1 || parents[0].TargetID != projectID {
		t.Fatalf("child_of edges of issue %s = %+v, %v, want the project edge", issue.ID, parents, err)
	}
	if events := outboxEventsAfter(t, env, mark); len(events) != 0 {
		t.Fatalf("the refused writes wrote outbox events %+v", events)
	}
}

// futureOperation returns an operation that starts one hour after the
// current time. A change event under it fails validation because its
// completion time precedes its start time.
func futureOperation(t *testing.T) audit.Event {
	t.Helper()
	operation, err := audit.RequireMutationContext(integration.WithTestOperation(t, context.Background()))
	if err != nil {
		t.Fatalf("create the audit operation: %v", err)
	}
	operation.Context.StartedAt = clock.Now().Add(time.Hour).UTC()
	return operation
}

// TestRuntimeAuditRelationshipReplacement replaces a stored relationship,
// moves an issue to another parent, and removes the relationship. Each
// change event must record the target identity and the actual old and new
// values that the transaction read and wrote.
func TestRuntimeAuditRelationshipReplacement(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	issue := createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	label := createSchemaNode(t, env, projectID, projectID, "label", nil)
	if err := env.NodeSvc.AddRelationship(env.Ctx, labelEdge(env, issue.ID, label.ID, uuid.New())); err != nil {
		t.Fatalf("add the first edge: %v", err)
	}
	stored := storedEdge(t, env, issue.ID, node.RelLabeledWith, label.ID)
	mark := outboxTail(t, env)
	replaceContext := integration.WithTestOperation(t, env.Ctx)
	if err := env.NodeSvc.AddRelationship(replaceContext, labelEdge(env, issue.ID, label.ID, uuid.New())); err != nil {
		t.Fatalf("replace the edge: %v", err)
	}
	replacement := storedEdge(t, env, issue.ID, node.RelLabeledWith, label.ID)
	update := requireOneEvent(t, outboxEventsAfter(t, env, mark), audit.VerbRelationshipUpdate, issue.ID)
	requireChangeEvent(t, update, operationIDOf(t, replaceContext), integration.TestActorID)
	requireRelationshipEvent(t, update, issue.ID, node.RelLabeledWith, label.ID)
	requireJSONEqual(t, "replaced old value", update.Delta.Before, stored)
	requireJSONEqual(t, "replaced new value", update.Delta.After, replacement)

	epicParent := storedEdge(t, env, issue.ID, node.RelChildOf, epic.ID)
	mark = outboxTail(t, env)
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: issue.ID, Name: nil, Props: map[string]json.RawMessage{"parent_id": quoted(projectID.String())},
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("move issue %s: %v", issue.ID, err)
	}
	moved := outboxEventsAfter(t, env, mark)
	requireOneEvent(t, moved, audit.VerbNodeUpdate, issue.ID)
	removedParent := requireOneEvent(t, moved, audit.VerbRelationshipRemove, issue.ID)
	addedParent := requireOneEvent(t, moved, audit.VerbRelationshipAdd, issue.ID)
	requireRelationshipEvent(t, removedParent, issue.ID, node.RelChildOf, epic.ID)
	requireRelationshipEvent(t, addedParent, issue.ID, node.RelChildOf, projectID)
	projectParent := storedEdge(t, env, issue.ID, node.RelChildOf, projectID)
	requireJSONEqual(t, "removed parent old value", removedParent.Delta.Before, epicParent)
	requireJSONEqual(t, "added parent new value", addedParent.Delta.After, projectParent)
	if len(removedParent.Delta.After) != 0 || len(addedParent.Delta.Before) != 0 {
		t.Fatalf("removed parent new value = %s and added parent old value = %s, want none",
			removedParent.Delta.After, addedParent.Delta.Before)
	}
	if removedParent.Context.OperationID != addedParent.Context.OperationID {
		t.Fatalf("parent change operations = %s and %s, want one operation",
			removedParent.Context.OperationID, addedParent.Context.OperationID)
	}

	mark = outboxTail(t, env)
	if err := env.NodeSvc.RemoveRelationship(env.Ctx, env.OrgID, issue.ID, node.RelLabeledWith, label.ID); err != nil {
		t.Fatalf("remove the edge: %v", err)
	}
	removed := requireOneEvent(t, outboxEventsAfter(t, env, mark), audit.VerbRelationshipRemove, issue.ID)
	requireJSONEqual(t, "removed old value", removed.Delta.Before, replacement)
	if len(removed.Delta.After) != 0 {
		t.Fatalf("removed edge new value = %s, want none", removed.Delta.After)
	}
}
