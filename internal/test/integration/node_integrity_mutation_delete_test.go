package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationDeleteRefusesChild checks a deletion plan for an
// issue with a comment under it. DeleteNode must refuse the plan with
// domain.ErrFailedPrecondition and preserve the stored issue and comment.
func TestNodeIntegrityMutationDeleteRefusesChild(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	comment := createSchemaNode(t, env, issue.ID, projectID, "comment", schemaProps("description", quoted("Child comment")))
	snapshot, _ := mutationSnapshot(t, env, issue.ID)

	_, err := env.Stores.Nodes.DeleteNode(env.Ctx, deletionPlan(snapshot))
	requireError(t, err, domain.ErrFailedPrecondition, "DeleteNode of an issue with a comment")
	requireUnchanged(t, env, snapshot)
	storedStoreView(t, env.Stores, comment.ID)
}

// TestNodeIntegrityMutationDeleteRaces checks prepared deletions of three leaf
// issues after a comment addition under the first, a rename of the second,
// and a mentioned_in relationship addition to the third. Each deletion must
// fail without deleting the issue. The first must fail with
// domain.ErrFailedPrecondition and the others with domain.ErrConflict.
func TestNodeIntegrityMutationDeleteRaces(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issues := make([]node.MutationSnapshot, 0, 3)
	for range 3 {
		issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
		snapshot, _ := mutationSnapshot(t, env, issue.ID)
		issues = append(issues, snapshot)
	}
	createSchemaNode(t, env, issues[0].Node.ID, projectID, "comment", schemaProps("description", quoted("Racing comment")))
	renamed := "Renamed before deletion"
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: issues[1].Node.ID, Name: &renamed, Props: nil,
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("rename issue %s: %v", issues[1].Node.ID, err)
	}
	if err := env.Stores.Relationships.Add(env.Ctx, &node.Relationship{
		OrgID: env.OrgID, SourceID: issues[0].Node.ID, RelationType: node.RelMentionedIn, TargetID: issues[2].Node.ID,
		CreatedBy: uuid.New(), CreatedAt: clock.Now().UTC(), Props: nil,
	}); err != nil {
		t.Fatalf("add a mentioned_in relationship to %s: %v", issues[2].Node.ID, err)
	}

	for position, want := range []error{domain.ErrFailedPrecondition, domain.ErrConflict, domain.ErrConflict} {
		_, err := env.Stores.Nodes.DeleteNode(env.Ctx, deletionPlan(issues[position]))
		requireError(t, err, want, "prepared DeleteNode of issue "+issues[position].Node.ID.String())
		if current, _ := mutationSnapshot(t, env, issues[position].Node.ID); !current.Exists {
			t.Fatalf("a refused deletion removed issue %s", issues[position].Node.ID)
		}
	}
}

// TestNodeIntegrityMutationDeletesLeaf checks deletion of a leaf issue through
// the service under an audit slot. The deletion must remove the issue, its
// view, and its child_of edge. The outbox must record exactly one node.delete
// event for the issue.
func TestNodeIntegrityMutationDeletesLeaf(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	actorID := uuid.New()
	ctx := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_delete_issue", actorID)

	result, err := env.NodeSvc.Delete(ctx, issue.ID, actorID)
	if err != nil || result.Deleted != 1 || result.State != node.SubtreeDeleteFinished {
		t.Fatalf("delete issue %s = %+v, %v, want one deleted node", issue.ID, result, err)
	}
	if snapshot, _ := mutationSnapshot(t, env, issue.ID); snapshot.Exists {
		t.Fatalf("issue %s exists after its deletion", issue.ID)
	}
	if view, err := env.Stores.Views.Get(env.Ctx, issue.ID); err != nil || view != nil {
		t.Fatalf("view of deleted issue %s = %v, %v, want none", issue.ID, view, err)
	}
	edges, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, projectID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges to project %s: %v", projectID, err)
	}
	for _, edge := range edges {
		if edge.SourceID == issue.ID {
			t.Fatalf("project %s keeps the child_of edge of deleted issue %s", projectID, issue.ID)
		}
	}
	outboxEvent(t, env.Stores, issue.ID, audit.VerbNodeDelete)
}
