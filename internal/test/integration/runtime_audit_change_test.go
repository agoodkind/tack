package integration_test

import (
	"context"
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
		NodeID:              issue.ID,
		Name:                &renamed,
		Props:               nil,
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		ActorID:             uuid.New(),
	}

	missing := context.Background()
	if _, err := env.NodeSvc.Update(missing, rename); !errors.Is(
		err,
		audit.ErrMissingMutationContext,
	) {
		t.Fatalf("update without an operation = %v, want audit.ErrMissingMutationContext", err)
	}

	if err := env.NodeSvc.AddRelationship(missing, edge); !errors.Is(
		err,
		audit.ErrMissingMutationContext,
	) {
		t.Fatalf(
			"add relationship without an operation = %v, want audit.ErrMissingMutationContext",
			err,
		)
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
		t.Fatalf(
			"issue %s after the refused writes = %+v, %v, want name %q",
			issue.ID,
			view,
			err,
			issue.Name,
		)
	}

	labels, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issue.ID,
		node.RelLabeledWith,
	)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labeled_with edges of issue %s = %+v, %v, want none", issue.ID, labels, err)
	}

	parents, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issue.ID,
		node.RelChildOf,
	)
	if err != nil || len(parents) != 1 || parents[0].TargetID != projectID {
		t.Fatalf(
			"child_of edges of issue %s = %+v, %v, want the project edge",
			issue.ID,
			parents,
			err,
		)
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
	operation, err := audit.RequireMutationContext(
		integration.WithTestOperation(t, context.Background()),
	)
	if err != nil {
		t.Fatalf("create the audit operation: %v", err)
	}

	operation.Context.StartedAt = clock.Now().Add(time.Hour).UTC()

	return operation
}
