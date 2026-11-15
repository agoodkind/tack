package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditDeleteResumeRun verifies that a resumed user delete does not create a service operation.
func TestRuntimeAuditDeleteResumeRun(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	root := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	createSchemaNode(
		t,
		env,
		root.ID,
		projectID,
		"comment",
		schemaProps("description", quoted("Resumed comment")),
	)
	job := &node.SubtreeDeleteJob{
		ID:        uuid.Must(uuid.NewV7()),
		OrgID:     env.OrgID,
		RootID:    root.ID,
		ActorID:   integration.TestActorID,
		Stack:     nil,
		Deleted:   0,
		UpdatedAt: time.Time{},
	}
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(env.Ctx, job); err != nil {
		t.Fatalf("starting the delete of issue %s failed: %v", root.ID, err)
	}

	if _, err := env.Stores.NodeDeleter.DeleteSubtreeStep(env.Ctx, job.ID); err != nil {
		t.Fatalf("running one step of job %s failed: %v", job.ID, err)
	}

	mark := outboxTail(t, env)
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0); err != nil ||
		finished != 1 {
		t.Fatalf("the resume pass finished %d jobs with error %v instead of one job", finished, err)
	}

	status, err := env.NodeSvc.DeleteStatus(env.Ctx, job.ID)
	if err != nil || status.State != node.SubtreeDeleteFinished {
		t.Fatalf("job %s has status %+v and error %v instead of finished", job.ID, status, err)
	}

	events := resumeEvents(t, env, mark)
	if len(events) != 0 {
		t.Fatalf("resume recorded %d separate service events", len(events))
	}

	mark = outboxTail(t, env)
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0); err != nil ||
		finished != 0 {
		t.Fatalf(
			"the second resume pass finished %d jobs with error %v instead of none",
			finished,
			err,
		)
	}

	if again := resumeEvents(t, env, mark); len(again) != 0 {
		t.Fatalf("the second resume pass recorded %d events instead of none", len(again))
	}
}

// resumeEvents returns the delete resumer events in the FoundationDB outbox
// after mark.
func resumeEvents(t *testing.T, env *integration.TestEnv, mark []byte) []audit.Event {
	t.Helper()
	var events []audit.Event

	for _, event := range outboxEventsAfter(t, env, mark) {
		if event.Verb == string(audit.VerbNodeSubtreeDeleteResume) {
			events = append(events, event)
		}
	}

	return events
}
