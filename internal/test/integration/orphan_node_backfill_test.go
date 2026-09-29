package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
)

// TestOrphanNodeBackfill deletes a project with the single-node store delete,
// the shape every public delete left before the cascade existed. The
// project's states, epic, and direct issues keep parent_id values that refer
// to the deleted project and have no parent edge. The dry run must report
// exactly those orphans and delete nothing. The executed run must delete the
// orphans and their descendants and write one node.delete ledger event per
// deleted node. A rerun must report zero orphans.
func TestOrphanNodeBackfill(t *testing.T) {
	env := SetupTestEnv(t)
	fixture := newCascadeFixture(t, env)
	orphans := directChildren(t, fixture.Project)
	if err := env.Stores.Nodes.Delete(env.Ctx, env.OrgID, fixture.Project); err != nil {
		t.Fatalf("delete project %s without its descendants: %v", fixture.Project, err)
	}
	drainOutbox(t, env)
	principal := audit.OperatorPrincipal{ID: uuid.New(), Email: "operator@example.com", Name: "Operator", Source: "test"}
	ctx := audit.WithOperatorPrincipal(env.Ctx, principal)
	service := env.NodeSvc

	planned, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, service, true)
	if err != nil {
		t.Fatalf("dry-run orphan backfill: %v", err)
	}
	if planned.Orphans != len(orphans) || planned.Deleted != 0 || !sameIDs(planned.NodeIDs, orphans) {
		t.Errorf("dry-run result = %+v, want the %d orphans %v and no deletes", planned, len(orphans), orphans)
	}
	requireNodesPresent(t, env, fixture.Descendants)

	applied, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, service, false)
	if err != nil {
		t.Fatalf("run orphan backfill: %v", err)
	}
	if applied.Orphans != len(orphans) || applied.Deleted != len(fixture.Descendants) {
		t.Fatalf("backfill result = %+v, want %d orphans and %d deleted nodes", applied, len(orphans), len(fixture.Descendants))
	}
	requireNodesGone(t, env, fixture.Descendants)
	requireNodesPresent(t, env, fixture.Kept)
	requireNoDanglingParents(t, env)
	requireOperatorDeleteEvents(t, env, fixture.Descendants, principal.ID)

	rerun, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, service, false)
	if err != nil {
		t.Fatalf("rerun orphan backfill: %v", err)
	}
	if rerun.Orphans != 0 || rerun.Deleted != 0 || len(rerun.NodeIDs) != 0 {
		t.Fatalf("rerun result = %+v, want zero orphans", rerun)
	}
}

// requireOperatorDeleteEvents requires exactly one node.delete outbox row by
// operatorID from the orphan backfill for each node in removed.
func requireOperatorDeleteEvents(t *testing.T, env *TestEnv, removed []uuid.UUID, operatorID uuid.UUID) {
	t.Helper()
	events := outboxEvents(t, env)
	recorded := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		if event.Verb != string(audit.VerbNodeDelete) || event.Actor.ID != operatorID || event.Context.Tool != "ops.backfill.once-orphan-nodes" {
			t.Fatalf("outbox row = %+v, want node.delete by operator %s from the orphan backfill", event, operatorID)
		}
		recorded = append(recorded, event.Entity.ID)
	}
	if !sameIDs(recorded, removed) {
		t.Fatalf("outbox rows describe nodes %v, want %v", recorded, removed)
	}
}

// sameIDs reports whether left and right contain the same IDs.
func sameIDs(left, right []uuid.UUID) bool {
	sortedLeft := slices.Clone(left)
	sortedRight := slices.Clone(right)
	slices.SortFunc(sortedLeft, compareUUID)
	slices.SortFunc(sortedRight, compareUUID)
	return slices.Equal(sortedLeft, sortedRight)
}
