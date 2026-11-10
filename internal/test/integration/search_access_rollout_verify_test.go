package integration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchAccessRolloutDualVersionWrites requires a node created during
// backfill to store both versions and their keys. A search under the
// stable version must find the node. The finished rollout must leave only
// the rotated version.
func TestSearchAccessRolloutDualVersionWrites(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	entryID := rolloutEntryPoint(t, fixture, workspace)
	kind := putRolloutKind(t, fixture, workspace.OrgID)
	query := "dual version meadow"
	existing := putRolloutCorpus(t, fixture, kind, entryID, query, 1)
	drainRolloutWork(t, fixture)

	beginAccessRollout(t, fixture, workspace.OrgID)
	written := putRolloutNode(t, fixture, kind, entryID, "Dual version write", query+" harbor")
	drainLiveWork(t, fixture)
	requireAccessRollout(t, currentAccessRollout(t, fixture, workspace.OrgID), searchaccess.StableVersion, bothAccessVersions, searchdomain.AccessBackfill)
	expected := expectedAccessKeys(t, fixture, workspace.OrgID, written)
	for _, page := range rolloutPages(t, fixture, []uuid.UUID{written}) {
		if !slices.Equal(page.Access.Versions, bothAccessVersions) || !slices.Equal(page.Access.Keys, expected) {
			t.Fatalf("page %s stores versions %v and keys %v instead of versions %v and keys %v", page.ID, page.Access.Versions, page.Access.Keys, bothAccessVersions, expected)
		}
	}
	requireNodesFound(t, traverseSearch(t, fixture, workspace, query, searchAs(t, fixture, workspace, query, "")), []uuid.UUID{existing[0], written})

	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, append(slices.Clone(existing), written)), []string{searchaccess.RotatedVersion})
	requireNodesFound(t, traverseSearch(t, fixture, workspace, query, searchAs(t, fixture, workspace, query, "")), []uuid.UUID{existing[0], written})
}

// TestSearchAccessRolloutGrantDuringVerification requires the rollout to
// delay activation while the test keeps a claim on the moved node's access
// work after a grant change during the affected pass. The rollout must
// preserve its generation and must not restart the authority scan. After the
// test yields the claim, the rollout must activate with both versions' new
// entry point keys at a higher access generation on the moved node's pages.
// Page text and semantic fields must remain unchanged.
func TestSearchAccessRolloutGrantDuringVerification(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, second := sameOrganizationWorkspaces(t, fixture)
	from, to := rolloutEntryPoint(t, fixture, first), rolloutEntryPoint(t, fixture, second)
	kind := putRolloutKind(t, fixture, first.OrgID)
	nodes := putRolloutCorpus(t, fixture, kind, from, "grant verification harbor", 3)
	moved := nodes[0]
	drainRolloutWork(t, fixture)
	before := rolloutPages(t, fixture, []uuid.UUID{moved})

	beginAccessRollout(t, fixture, first.OrgID)
	affected := runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.AffectedPass
	})
	if affected.Activated() {
		t.Fatalf("The rollout activated before the grant change: %+v", affected)
	}
	moveRolloutNode(t, fixture, moved, to)
	held := holdAccessWork(t, fixture, moved)

	restarted := false
	for range rollbackWaitSlices {
		runRolloutSlice(t, fixture.worker)
		current := currentAccessRollout(t, fixture, first.OrgID)
		restarted = restarted || current.Phase == searchdomain.AccessBackfill
		if current.Activated() {
			t.Fatalf("The rollout activated while the test kept the claim on the moved node's access work: %+v", current)
		}
	}
	releaseAccessWork(t, fixture, held)
	activated := runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		restarted = restarted || rollout.Phase == searchdomain.AccessBackfill
		return rollout.Activated()
	})
	if restarted || activated.Generation != affected.Generation {
		t.Fatalf("The grant change must not restart the authority scan or change the generation: restarted=%t, activated generation=%d, affected pass generation=%d", restarted, activated.Generation, affected.Generation)
	}
	expected := expectedAccessKeys(t, fixture, first.OrgID, moved)
	after := rolloutPages(t, fixture, []uuid.UUID{moved})
	for position, page := range after {
		previous := before[position]
		if !slices.Equal(page.Access.Versions, bothAccessVersions) || !slices.Equal(page.Access.Keys, expected) ||
			slices.Equal(page.Access.Keys, previous.Access.Keys) || page.Access.Generation <= previous.Access.Generation {
			t.Fatalf("Page %s must store both versions with changed keys and a higher access generation: access=%+v, previous access=%+v, expected keys=%v", page.ID, page.Access, previous.Access, expected)
		}
	}
	requirePagesPreserved(t, before, after)

	final := runRolloutUntil(t, fixture, first.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, nodes), []string{searchaccess.RotatedVersion})
}

// TestSearchAccessRolloutAffectedOverflow creates 260 nodes under one entry
// point before the rollout and moves them to a second entry point of the
// same organization during the affected pass. It requires the affected set
// to contain more than 256 nodes. Each step before activation must keep the
// rollout in verifying and in the affected pass at the same generation. At
// activation, every page of every moved node must store both versions and
// the new entry point keys for both versions. The rollout must then finish
// with only the rotated version on every page.
func TestSearchAccessRolloutAffectedOverflow(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, second := sameOrganizationWorkspaces(t, fixture)
	from, to := rolloutEntryPoint(t, fixture, first), rolloutEntryPoint(t, fixture, second)
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, first.OrgID), from, "affected overflow lantern", overflowGrantNodes)
	drainRolloutWork(t, fixture)

	begun := beginAccessRollout(t, fixture, first.OrgID)
	runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.AffectedPass
	})
	for _, nodeID := range nodes {
		moveRolloutNode(t, fixture, nodeID, to)
	}
	if affected := affectedNodes(t, fixture, first.OrgID); len(affected.Nodes) <= affectedSetBound {
		t.Fatalf("Affected set size %d after the moves must exceed the bound of %d", len(affected.Nodes), affectedSetBound)
	}
	activated := runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		if rollout.Activated() {
			return true
		}
		if !rollout.AffectedPass || rollout.Phase != searchdomain.AccessVerifying || rollout.Generation != begun.Generation {
			t.Fatalf("Rollout %+v must keep its generation through activation and remain in verifying and in the affected pass until activation", rollout)
		}
		return false
	})
	if activated.Generation != begun.Generation {
		t.Fatalf("Rollout %+v must keep its generation through activation and remain in verifying and in the affected pass until activation", activated)
	}
	expected := make(map[uuid.UUID][]string, len(nodes))
	for _, nodeID := range nodes {
		expected[nodeID] = expectedAccessKeys(t, fixture, first.OrgID, nodeID)
	}
	for _, page := range rolloutPages(t, fixture, nodes) {
		if !slices.Equal(page.Access.Versions, bothAccessVersions) || !slices.Equal(page.Access.Keys, expected[page.NodeID]) {
			t.Fatalf("Page %s of node %s must store both versions and their new entry point keys at activation: access=%+v, wanted keys=%v", page.ID, page.NodeID, page.Access, expected[page.NodeID])
		}
	}

	final := runRolloutUntil(t, fixture, first.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, nodes), []string{searchaccess.RotatedVersion})
}

// sameOrganizationWorkspaces returns two bootstrapped workspaces of one
// organization.
func sameOrganizationWorkspaces(t *testing.T, fixture rolloutFixture) (datagen.WorkspaceIdentity, datagen.WorkspaceIdentity) {
	t.Helper()
	for position, workspace := range fixture.workspaces {
		for _, other := range fixture.workspaces[position+1:] {
			if other.OrgID == workspace.OrgID {
				return workspace, other
			}
		}
	}
	t.Fatal("the fixture does not have an organization with two workspaces")
	return datagen.WorkspaceIdentity{}, datagen.WorkspaceIdentity{}
}

// otherOrganizationWorkspace returns a workspace outside the first
// workspace's organization.
func otherOrganizationWorkspace(t *testing.T, fixture rolloutFixture) datagen.WorkspaceIdentity {
	t.Helper()
	for _, workspace := range fixture.workspaces {
		if workspace.OrgID != fixture.workspaces[0].OrgID {
			return workspace
		}
	}
	t.Fatal("the fixture does not have a workspace in another organization")
	return datagen.WorkspaceIdentity{}
}
