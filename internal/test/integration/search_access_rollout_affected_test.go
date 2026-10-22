package integration_test

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/service"
)

// concurrentGrantRecordings is the number of recordings from two moves
// between the same two entry points. Each move records the moved node and
// both entry points.
const concurrentGrantRecordings = 6

// TestSearchAccessRolloutDeletionDuringVerification deletes a node during
// the affected pass and requires the affected set to include it. It claims
// the deleted node's fanout access work and requires activation to wait
// until the test yields that claim.
func TestSearchAccessRolloutDeletionDuringVerification(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	entryID := rolloutEntryPoint(t, fixture, workspace)
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, workspace.OrgID), entryID, "deleted rollout anchor", 2)
	deleted := nodes[0]
	drainRolloutWork(t, fixture)

	beginAccessRollout(t, fixture, workspace.OrgID)
	runRolloutUntil(t, fixture, workspace.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.AffectedPass
	})
	if err := fixture.stores.Nodes.Delete(t.Context(), workspace.OrgID, deleted); err != nil {
		t.Fatalf("Deleting node %s failed: %v", deleted, err)
	}
	if affected := affectedNodes(t, fixture, workspace.OrgID); !slices.Contains(affected.Nodes, deleted) {
		t.Fatalf("Affected set %+v must contain deleted node %s", affected, deleted)
	}
	held := holdAccessWork(t, fixture, deleted)
	for range rollbackWaitSlices {
		runRolloutSlice(t, fixture.worker)
		if current := currentAccessRollout(t, fixture, workspace.OrgID); current.Activated() {
			t.Fatalf("Rollout %+v activated while the test kept the claim on the deleted node's fanout access work", current)
		}
	}
	releaseAccessWork(t, fixture, held)
	runRolloutUntil(t, fixture, workspace.OrgID, searchdomain.AccessRollout.Activated)
	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, nodes[1:]), []string{searchaccess.RotatedVersion})
}

// TestSearchAccessRolloutGrantDuringBackfill moves a node during backfill
// and requires zero recordings and an empty affected set. At activation, the
// moved node's pages must store the new keys of both versions.
func TestSearchAccessRolloutGrantDuringBackfill(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, second := sameOrganizationWorkspaces(t, fixture)
	from, to := rolloutEntryPoint(t, fixture, first), rolloutEntryPoint(t, fixture, second)
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, first.OrgID), from, "backfill grant compass", 2)
	moved := nodes[0]
	drainRolloutWork(t, fixture)

	begun := beginAccessRollout(t, fixture, first.OrgID)
	moveRolloutNode(t, fixture, moved, to)
	affected := affectedNodes(t, fixture, first.OrgID)
	if affected.Recorded != 0 || len(affected.Nodes) != 0 || currentAccessRollout(t, fixture, first.OrgID).Phase != searchdomain.AccessBackfill {
		t.Fatalf("Affected nodes %+v must have zero recordings and an empty set during backfill after rollout %+v begins", affected, begun)
	}
	runRolloutUntil(t, fixture, first.OrgID, searchdomain.AccessRollout.Activated)
	expected := expectedAccessKeys(t, fixture, first.OrgID, moved)
	for _, page := range rolloutPages(t, fixture, []uuid.UUID{moved}) {
		if !slices.Equal(page.Access.Versions, bothAccessVersions) || !slices.Equal(page.Access.Keys, expected) {
			t.Fatalf("Page %s stores access %+v instead of both versions with keys %v", page.ID, page.Access, expected)
		}
	}
}

// TestSearchAccessRolloutConcurrentGrants moves two nodes concurrently
// during the affected pass and requires both moves to commit. The recording
// counter must equal six, and the affected set must include both moved nodes
// and both entry points.
func TestSearchAccessRolloutConcurrentGrants(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, second := sameOrganizationWorkspaces(t, fixture)
	from, to := rolloutEntryPoint(t, fixture, first), rolloutEntryPoint(t, fixture, second)
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, first.OrgID), from, "concurrent grant meadow", 2)
	drainRolloutWork(t, fixture)

	beginAccessRollout(t, fixture, first.OrgID)
	runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.AffectedPass
	})
	errs := make([]error, len(nodes))
	var group sync.WaitGroup
	for position, nodeID := range nodes {
		group.Go(func() { errs[position] = moveNodeConcurrently(t, fixture, nodeID, to) })
	}
	group.Wait()
	for position, err := range errs {
		if err != nil {
			t.Fatalf("Moving node %s concurrently failed: %v", nodes[position], err)
		}
	}
	affected := affectedNodes(t, fixture, first.OrgID)
	for _, nodeID := range append(slices.Clone(nodes), from, to) {
		if !slices.Contains(affected.Nodes, nodeID) {
			t.Fatalf("Affected set %+v must contain node %s", affected, nodeID)
		}
	}
	if affected.Recorded != concurrentGrantRecordings || len(affected.Nodes) != len(nodes)+2 {
		t.Fatalf("Affected nodes %+v must have %d recordings and four distinct nodes", affected, concurrentGrantRecordings)
	}
}

// moveNodeConcurrently moves nodeID under the entry node specified by to
// using [service.NodeService.Update] and returns the error. It does not call
// a [testing.T] method that ends the test. A goroutine may run it.
func moveNodeConcurrently(t *testing.T, fixture rolloutFixture, nodeID, to uuid.UUID) error {
	target, err := json.Marshal(to.String())
	if err != nil {
		return err
	}
	_, err = rolloutNodeService(fixture).Update(t.Context(), service.UpdateInput{
		NodeID: nodeID, Props: map[string]json.RawMessage{"parent_id": target, "scope_id": target},
	})
	return err
}

// affectedNodes reads the affected nodes of authorityID through the
// production rollout store.
func affectedNodes(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID) searchdomain.AffectedNodes {
	t.Helper()
	affected, err := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet()).AffectedNodes(t.Context(), authorityID)
	if err != nil {
		t.Fatalf("Reading affected nodes of authority %s failed: %v", authorityID, err)
	}
	return affected
}
