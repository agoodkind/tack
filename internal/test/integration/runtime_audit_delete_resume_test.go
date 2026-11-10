package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/domain/node"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
)

// subtreeDeleteStepEvidence is the job state that a delete resumer step
// records.
type subtreeDeleteStepEvidence struct {
	JobID   string `json:"job_id"`
	RootID  string `json:"root_id"`
	State   string `json:"state"`
	Deleted int    `json:"deleted"`
}

// TestRuntimeAuditDeleteResumeRun starts an issue subtree delete, runs one
// step, and resumes the job with the production delete resumer recording.
// The FoundationDB outbox must contain one resume run for the job under the
// delete resumer service actor: an intent, a step intent with the running
// job state, a completed step result with the finished job state and the
// deleted node count, and a completed result. A second resume pass must not
// record another run.
func TestRuntimeAuditDeleteResumeRun(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	root := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	createSchemaNode(t, env, root.ID, projectID, "comment", schemaProps("description", quoted("Resumed comment")))
	job := &node.SubtreeDeleteJob{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: root.ID, ActorID: integration.TestActorID,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(env.Ctx, job); err != nil {
		t.Fatalf("starting the delete of issue %s failed: %v", root.ID, err)
	}
	if _, err := env.Stores.NodeDeleter.DeleteSubtreeStep(env.Ctx, job.ID); err != nil {
		t.Fatalf("running one step of job %s failed: %v", job.ID, err)
	}
	mark := outboxTail(t, env)
	recording := appruntime.DeleteResumerAudit(env.Stores)
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0, recording); err != nil || finished != 1 {
		t.Fatalf("the resume pass finished %d jobs with error %v instead of one job", finished, err)
	}
	status, err := env.NodeSvc.DeleteStatus(env.Ctx, job.ID)
	if err != nil || status.State != node.SubtreeDeleteFinished {
		t.Fatalf("job %s has status %+v and error %v instead of finished", job.ID, status, err)
	}
	events := resumeEvents(t, env, mark)
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("the resume pass recorded %d run intents instead of 1", len(intents))
	}
	intent := intents[0]
	if intent.Actor.Type != audit.ActorService || intent.Actor.ID != cli.ServiceActorID(appruntime.DeleteResumerService) ||
		intent.Context.Target == nil || intent.Context.Target.Reference != "foundationdb/subtree-delete/"+job.ID.String() {
		t.Fatalf("the run intent records actor %+v and target %+v instead of the delete resumer and job %s",
			intent.Actor, intent.Context.Target, job.ID)
	}
	operationID := intent.Context.OperationID
	planned := decodeResumeStep(t, eventsInPhase(events, audit.PhaseIntent, operationID))
	if planned.JobID != job.ID.String() || planned.RootID != root.ID.String() || planned.State != string(node.SubtreeDeleteRunning) {
		t.Fatalf("the step intent records %+v instead of the running job %s", planned, job.ID)
	}
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	observed := decodeResumeStep(t, stepResults)
	if stepResults[0].Outcome != audit.OutcomeCompleted || observed.State != string(node.SubtreeDeleteFinished) || observed.Deleted != status.Deleted {
		t.Fatalf("the step result is %s with %+v instead of completed with the finished job and %d deleted nodes",
			stepResults[0].Outcome, observed, status.Deleted)
	}
	requireCompletedResult(t, events, operationID)
	mark = outboxTail(t, env)
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0, recording); err != nil || finished != 0 {
		t.Fatalf("the second resume pass finished %d jobs with error %v instead of none", finished, err)
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

// decodeResumeStep requires one step event and decodes its evidence.
func decodeResumeStep(t *testing.T, steps []audit.Event) subtreeDeleteStepEvidence {
	t.Helper()
	if len(steps) != 1 {
		t.Fatalf("the resume run recorded %d step events in one phase instead of 1", len(steps))
	}
	var evidence subtreeDeleteStepEvidence
	if err := json.Unmarshal(steps[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding the step evidence of event %s failed: %v", steps[0].EventID, err)
	}
	return evidence
}
