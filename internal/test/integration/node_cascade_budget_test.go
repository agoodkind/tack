package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// cascadeBudgetComments sizes a subtree that needs more delete steps than
// one request budget runs. Each comment takes two steps.
const cascadeBudgetComments = 2000

// TestDeleteLargerThanOneBudget deletes a project with one issue and 2000
// comments under the tool wrapper's audit slot. The request must return a
// running job with the audit slot marked written, and the status read must
// report the job running. A resume pass must finish the job. The status read
// must then report it finished with every node counted, and the outbox must
// contain exactly one node.delete row per deleted node.
func TestDeleteLargerThanOneBudget(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Large", workspace.ID, workspace.ID, actor)
	issueID := createTestIssue(t, env, project.ID, "Busy issue")
	removed := append([]uuid.UUID{project.ID}, directChildren(t, project.ID)...)
	for range cascadeBudgetComments {
		comment := mustCreate(t, env, service.CreateInput{
			ParentID: issueID, ScopeID: project.ID, NodeTypeKey: "comment", Name: "Comment", ActorID: actor,
		})
		removed = append(removed, comment.ID)
	}
	drainOutbox(t, env)
	ctx := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_delete_project", actor)

	result, err := env.NodeSvc.Delete(ctx, project.ID, actor)
	if err != nil {
		t.Fatalf("delete project %s: %v", project.ID, err)
	}
	if result.State != node.SubtreeDeleteRunning || result.Deleted >= len(removed) {
		t.Fatalf("delete result = %+v, want a running job with fewer than %d deleted nodes", result, len(removed))
	}
	if !auditintent.Committed(ctx) {
		t.Fatal("the started job must mark the staged row written; the tool wrapper then records no second row")
	}
	running, err := env.NodeSvc.DeleteStatus(env.Ctx, result.JobID)
	if err != nil || running.State != node.SubtreeDeleteRunning || running.Deleted != result.Deleted {
		t.Fatalf("status after the request = %+v, %v; want running with %d deleted nodes", running, err, result.Deleted)
	}
	requireNodesPresent(t, env, []uuid.UUID{project.ID})

	finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0)
	if err != nil || finished != 1 {
		t.Fatalf("resume = %d, %v; want 1 finished job", finished, err)
	}
	status, err := env.NodeSvc.DeleteStatus(env.Ctx, result.JobID)
	if err != nil || status.State != node.SubtreeDeleteFinished || status.Deleted != len(removed) {
		t.Fatalf("status after the resume = %+v, %v; want finished with %d deleted nodes", status, err, len(removed))
	}
	requireNodesGone(t, env, removed)
	requireNodesPresent(t, env, []uuid.UUID{workspace.ID})
	requireNoDanglingParents(t, env)
	requireDeleteEvents(t, env, removed, actor)
}
