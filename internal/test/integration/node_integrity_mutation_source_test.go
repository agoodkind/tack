package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationRejectsStaleSource checks a prepared issue rename
// after a service rename. ApplyMutation must refuse the prepared rename and a
// plan that requires an absent node with domain.ErrConflict. The issue must
// retain the service rename.
func TestNodeIntegrityMutationRejectsStaleSource(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	stale, schema := mutationSnapshot(t, env, issue.ID)
	renamed := "Renamed by the service"
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: issue.ID, Name: &renamed, Props: nil,
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("rename issue %s: %v", issue.ID, err)
	}
	current, _ := mutationSnapshot(t, env, issue.ID)

	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, renamePlan(stale, schema, "Stale rename", nil))
	requireError(t, err, domain.ErrConflict, "ApplyMutation with a stale source")
	absent := renamePlan(current, schema, "Absent rename", nil)
	absent.Source = node.SourceExpectation{Absent: true, PrimaryDigest: node.Digest{}}
	_, err = env.Stores.Nodes.ApplyMutation(env.Ctx, absent)
	requireError(t, err, domain.ErrConflict, "ApplyMutation that expects an absent node")
	requireUnchanged(t, env, current)
	if got := storedStoreView(t, env.Stores, issue.ID).Name; got != renamed {
		t.Fatalf("issue name = %q, want %q", got, renamed)
	}
}

// TestNodeIntegrityMutationRejectsStaleGraph checks a prepared issue move
// under an epic and a prepared rename that requires the issue state to match.
// After renames of the epic and state, ApplyMutation must refuse both plans
// with domain.ErrConflict. It must also refuse a plan that omits an ancestor
// of the new parent and leave the issue unchanged.
func TestNodeIntegrityMutationRejectsStaleGraph(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	stateID := stateByName(t, env, projectID, "Todo")
	source, schema := mutationSnapshot(t, env, issue.ID)
	move := movePlan(t, env, source, schema, epic.ID)
	state, _ := mutationSnapshot(t, env, stateID)
	reference := renamePlan(source, schema, "Reference rename", []node.GraphRecord{{NodeID: stateID, PrimaryDigest: state.PrimaryDigest}})
	unlisted := movePlan(t, env, source, schema, epic.ID)
	unlisted.Graph.Records = unlisted.Graph.Records[:1]
	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, unlisted)
	requireError(t, err, domain.ErrConflict, "ApplyMutation without the ancestors of the parent")

	for _, renamed := range []uuid.UUID{epic.ID, stateID} {
		name := "Renamed " + renamed.String()
		if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
			NodeID: renamed, Name: &name, Props: nil,
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
		}); err != nil {
			t.Fatalf("rename %s: %v", renamed, err)
		}
	}
	_, err = env.Stores.Nodes.ApplyMutation(env.Ctx, move)
	requireError(t, err, domain.ErrConflict, "ApplyMutation with a stale parent digest")
	_, err = env.Stores.Nodes.ApplyMutation(env.Ctx, reference)
	requireError(t, err, domain.ErrConflict, "ApplyMutation with a stale reference digest")
	requireUnchanged(t, env, source)
}

// TestNodeIntegrityMutationRefusesReservedParentRelation checks a plan that
// adds a child_of relationship through Relationships. ApplyMutation must
// refuse the plan with node.ErrReservedParentRelation and leave the issue and
// its edges unchanged.
func TestNodeIntegrityMutationRefusesReservedParentRelation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	source, schema := mutationSnapshot(t, env, issue.ID)
	plan := renamePlan(source, schema, "Reserved edge", nil)
	plan.Relationships.Add = []*node.Relationship{{
		OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelChildOf, TargetID: epic.ID,
		CreatedBy: uuid.New(), CreatedAt: source.Node.CreatedAt, Props: nil,
	}}

	_, err := env.Stores.Nodes.ApplyMutation(env.Ctx, plan)
	requireError(t, err, node.ErrReservedParentRelation, "ApplyMutation with a child_of relationship")
	requireUnchanged(t, env, source)
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != projectID {
		t.Fatalf("child_of edges of %s = %v, %v, want one edge to project %s", issue.ID, edges, err, projectID)
	}
}
