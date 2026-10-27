package integration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditCascadeEvidence starts an issue subtree delete under one
// operation, runs one step, and resumes the job under a different operation.
// Every change event must record the actor and operation ID of the request
// that started the job. Each node.delete event for the issue or comment must
// include the complete stored record. The child's node.update event must
// record its old and new parent_id values. The relationship.add and
// relationship.remove events must identify the new and old child_of edges.
func TestRuntimeAuditCascadeEvidence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	root := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	child := createSchemaNode(t, env, root.ID, projectID, "issue", nil)
	comment := createSchemaNode(t, env, root.ID, projectID, "comment", schemaProps("description", quoted("Deleted comment")))
	storedRoot := storedNodeRecord(t, env, root.ID)
	storedComment := storedNodeRecord(t, env, comment.ID)
	mark := outboxTail(t, env)

	jobActor := uuid.New()
	operation, err := audit.NewMutationOperation(audit.Actor{
		Type: audit.ActorUser, ID: jobActor, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}, audit.SourceMCP, "tack_delete_issue")
	if err != nil {
		t.Fatalf("create the job operation: %v", err)
	}
	jobContext, err := audit.WithMutationContext(env.Ctx, operation)
	if err != nil {
		t.Fatalf("attach the job operation: %v", err)
	}
	job := &node.SubtreeDeleteJob{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: root.ID, ActorID: jobActor,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(jobContext, job); err != nil {
		t.Fatalf("start the delete of issue %s: %v", root.ID, err)
	}
	if _, err := env.Stores.NodeDeleter.DeleteSubtreeStep(jobContext, job.ID); err != nil {
		t.Fatalf("run one step of job %s: %v", job.ID, err)
	}
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0, env.LedgerResumeAudit()); err != nil || finished != 1 {
		t.Fatalf("resume = %d, %v, want 1 finished job", finished, err)
	}

	events := outboxEventsAfter(t, env, mark)
	eventIDs := map[uuid.UUID]struct{}{}
	for _, event := range events {
		requireChangeEvent(t, event, operation.Context.OperationID, jobActor)
		eventIDs[event.EventID] = struct{}{}
	}
	if len(eventIDs) != len(events) {
		t.Fatalf("%d change events share event IDs: %d distinct IDs", len(events), len(eventIDs))
	}
	requireJSONEqual(t, "deleted root record", requireOneEvent(t, events, audit.VerbNodeDelete, root.ID).Delta.Before, storedRoot)
	requireJSONEqual(t, "deleted comment record", requireOneEvent(t, events, audit.VerbNodeDelete, comment.ID).Delta.Before, storedComment)

	moved := requireOneEvent(t, events, audit.VerbNodeUpdate, child.ID)
	if !strings.Contains(string(moved.Delta.Before), root.ID.String()) || !strings.Contains(string(moved.Delta.After), projectID.String()) {
		t.Fatalf("moved child record before %s after %s, want parent %s replaced by %s", moved.Delta.Before, moved.Delta.After, root.ID, projectID)
	}
	if !slices.Contains(moved.Delta.Changed, "/props/parent_id") {
		t.Fatalf("moved child changed fields = %v, want /props/parent_id", moved.Delta.Changed)
	}
	requireRelationshipEvent(t, requireOneEvent(t, events, audit.VerbRelationshipAdd, child.ID), child.ID, node.RelChildOf, projectID)
	removedEdge := requireOneEvent(t, events, audit.VerbRelationshipRemove, child.ID)
	requireRelationshipEvent(t, removedEdge, child.ID, node.RelChildOf, root.ID)
	if len(removedEdge.Delta.Before) == 0 {
		t.Fatalf("removed child_of edge of the moved child = %+v, want its stored value", removedEdge)
	}
}
