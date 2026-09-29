package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// cascadeFanOut exceeds the edges one subtree delete step reads from one
// node.
const cascadeFanOut = 105

// TestDeleteSplitsHighFanOutAcrossSteps deletes a label with more incoming
// edges than one step clears, then a project with more children than one
// step reads. The label delete must keep every labeled issue and clear every
// labeled_with edge. The project delete must remove every child.
func TestDeleteSplitsHighFanOutAcrossSteps(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Wide", workspace.ID, workspace.ID, actor)
	label := mustCreate(t, env, service.CreateInput{
		ParentID: workspace.ID, ScopeID: workspace.ID, NodeTypeKey: "label", Name: "wide", ActorID: actor,
	})
	issues := make([]uuid.UUID, 0, cascadeFanOut)
	for range cascadeFanOut {
		issueID := createTestIssue(t, env, project.ID, "Wide issue")
		if err := env.NodeSvc.AddRelationship(env.Ctx, &node.Relationship{
			OrgID: env.OrgID, SourceID: issueID, RelationType: node.RelLabeledWith, TargetID: label.ID, CreatedBy: actor,
		}); err != nil {
			t.Fatalf("label issue %s: %v", issueID, err)
		}
		issues = append(issues, issueID)
	}

	labelDeleted, err := env.NodeSvc.Delete(env.Ctx, label.ID, actor)
	if err != nil || labelDeleted != 1 {
		t.Fatalf("delete label = %d, %v; want 1 deleted node", labelDeleted, err)
	}
	requireNodesPresent(t, env, issues)
	for _, issueID := range issues {
		labels, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issueID, node.RelLabeledWith)
		if err != nil {
			t.Fatalf("list labels of issue %s: %v", issueID, err)
		}
		if len(labels) != 0 {
			t.Fatalf("issue %s keeps %d labeled_with edges to the deleted label", issueID, len(labels))
		}
	}

	states, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", jsonStr(project.ID.String()))
	if err != nil {
		t.Fatalf("list states of project %s: %v", project.ID, err)
	}
	projectDeleted, err := env.NodeSvc.Delete(env.Ctx, project.ID, actor)
	if err != nil {
		t.Fatalf("delete project %s: %v", project.ID, err)
	}
	if want := 1 + len(states) + cascadeFanOut; projectDeleted != want {
		t.Fatalf("delete project reported %d deleted nodes, want %d", projectDeleted, want)
	}
	requireNodesGone(t, env, append([]uuid.UUID{project.ID}, issues...))
	requireNodesPresent(t, env, []uuid.UUID{workspace.ID})
	requireNoDanglingParents(t, env)
}

// TestResumeFinishesAnInterruptedDelete stores a subtree delete job and runs
// one step of it, the state a runner leaves when its process stops. A
// resume pass with a one-hour staleness bound must leave the fresh job alone.
// A resume pass with no staleness bound must finish the job and delete every
// descendant.
func TestResumeFinishesAnInterruptedDelete(t *testing.T) {
	env := SetupTestEnv(t)
	fixture := newCascadeFixture(t, env)
	job := &node.SubtreeDeleteJob{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: fixture.Project, AuditTemplate: nil,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(env.Ctx, job); err != nil {
		t.Fatalf("start the delete of project %s: %v", fixture.Project, err)
	}
	if _, err := env.Stores.NodeDeleter.DeleteSubtreeStep(env.Ctx, job.ID, nil); err != nil {
		t.Fatalf("run one step of job %s: %v", job.ID, err)
	}

	fresh, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, time.Hour)
	if err != nil || fresh != 0 {
		t.Fatalf("resume with a one-hour bound = %d, %v; want 0 finished jobs", fresh, err)
	}
	requireNodesPresent(t, env, []uuid.UUID{fixture.Project})

	finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0)
	if err != nil || finished != 1 {
		t.Fatalf("resume with no bound = %d, %v; want 1 finished job", finished, err)
	}
	requireNodesGone(t, env, append([]uuid.UUID{fixture.Project}, fixture.Descendants...))
	requireNodesPresent(t, env, fixture.Kept)
	requireNoDanglingParents(t, env)
	jobs, err := env.Stores.NodeDeleter.SubtreeDeletes(env.Ctx, uuid.Nil, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("job records after the resume = %d, %v; want none", len(jobs), err)
	}
}
