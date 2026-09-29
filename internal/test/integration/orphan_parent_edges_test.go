package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
)

// TestOrphanBackfillRemovesExtraParentEdges stores an issue under a project
// with three more child_of edges, to a cycle, a module, and an epic, written
// straight to the relationship store. That is the stored shape a client made
// through tack_add_relationship before the tool refused child_of. The dry
// run must keep all four edges. The executed run must remove the three
// edges that do not target the issue's parent_id, keep the issue and its
// parent edge, delete nothing, and write one relationship.remove ledger row
// per removed edge. A rerun must change nothing.
func TestOrphanBackfillRemovesExtraParentEdges(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Edges", workspace.ID, workspace.ID, actor)
	issueID := createTestIssue(t, env, project.ID, "Issue with extra parents")
	for _, typeKey := range []string{"cycle", "module", "epic"} {
		container := mustCreate(t, env, service.CreateInput{
			ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: typeKey, Name: "Container " + typeKey, ActorID: actor,
		})
		if err := env.Stores.Relationships.Add(env.Ctx, &node.Relationship{
			OrgID: env.OrgID, SourceID: issueID, RelationType: node.RelChildOf, TargetID: container.ID,
			CreatedBy: actor, CreatedAt: time.Now().UTC(), Props: nil,
		}); err != nil {
			t.Fatalf("add child_of edge to %s: %v", typeKey, err)
		}
	}
	requireParentEdges(t, env, issueID, 4)
	drainOutbox(t, env)
	principal := audit.OperatorPrincipal{ID: uuid.New(), Email: "operator@example.com", Name: "Operator", Source: "test"}
	ctx := audit.WithOperatorPrincipal(env.Ctx, principal)

	planned, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, env.NodeSvc, true)
	if err != nil {
		t.Fatalf("dry-run orphan backfill: %v", err)
	}
	if planned.ExtraParentEdges != 3 || planned.RemovedEdges != 0 || planned.Orphans != 0 {
		t.Errorf("dry-run result = %+v, want 3 extra parent edges, none removed, and no orphans", planned)
	}
	requireParentEdges(t, env, issueID, 4)

	applied, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, env.NodeSvc, false)
	if err != nil {
		t.Fatalf("run orphan backfill: %v", err)
	}
	if applied.ExtraParentEdges != 3 || applied.RemovedEdges != 3 || applied.Deleted != 0 {
		t.Errorf("backfill result = %+v, want 3 extra parent edges removed and no deletes", applied)
	}
	requireParentEdges(t, env, issueID, 1)
	requireNodesPresent(t, env, []uuid.UUID{issueID, project.ID})
	removals := 0
	for _, event := range outboxEvents(t, env) {
		if event.Verb == string(audit.VerbRelationshipRemove) && event.Actor.ID == principal.ID && event.Entity.ID == issueID {
			removals++
		}
	}
	if removals != 3 {
		t.Errorf("relationship.remove ledger rows for issue %s = %d, want 3", issueID, removals)
	}

	rerun, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, env.NodeSvc, false)
	if err != nil {
		t.Fatalf("rerun orphan backfill: %v", err)
	}
	if rerun.ExtraParentEdges != 0 || rerun.RemovedEdges != 0 || rerun.Orphans != 0 {
		t.Errorf("rerun result = %+v, want no extra parent edges and no orphans", rerun)
	}
}

// requireParentEdges requires nodeID to have want child_of edges, and one of
// them to target the node's parent_id.
func requireParentEdges(t *testing.T, env *TestEnv, nodeID uuid.UUID, want int) {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges of node %s: %v", nodeID, err)
	}
	if len(edges) != want {
		t.Fatalf("node %s has %d child_of edges, want %d", nodeID, len(edges), want)
	}
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	parentID := stringPropOf(view, "parent_id")
	for _, edge := range edges {
		if edge.TargetID.String() == parentID {
			return
		}
	}
	t.Fatalf("node %s has no child_of edge to its parent_id %s", nodeID, parentID)
}
