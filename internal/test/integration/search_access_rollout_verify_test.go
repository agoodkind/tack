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

// TestSearchAccessRolloutGrantDuringVerification moves a node to another
// entry point after verification begins. The rollout must return to
// backfill and record a higher permission event boundary before
// activation. At activation, the node's pages must store the new entry
// point keys of both versions at a higher access generation and preserve
// text and semantic fields.
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
	verifying := runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.Phase == searchdomain.AccessVerifying
	})
	if verifying.Activated() {
		t.Fatalf("rollout activated before the grant change: %+v", verifying)
	}
	moveRolloutNode(t, fixture, moved, to)

	repeatedScan := false
	activated := runRolloutUntil(t, fixture, first.OrgID, func(rollout searchdomain.AccessRollout) bool {
		repeatedScan = repeatedScan || rollout.Phase == searchdomain.AccessBackfill
		return rollout.Activated()
	})
	if !repeatedScan || activated.PermissionEventVersion <= verifying.PermissionEventVersion {
		t.Fatalf("grant verification must repeat the scan and increase the boundary: repeated scan %t, activation boundary %d, previous boundary %d", repeatedScan, activated.PermissionEventVersion, verifying.PermissionEventVersion)
	}
	expected := expectedAccessKeys(t, fixture, first.OrgID, moved)
	after := rolloutPages(t, fixture, []uuid.UUID{moved})
	for position, page := range after {
		previous := before[position]
		if !slices.Equal(page.Access.Versions, bothAccessVersions) || !slices.Equal(page.Access.Keys, expected) ||
			slices.Equal(page.Access.Keys, previous.Access.Keys) || page.Access.Generation <= previous.Access.Generation {
			t.Fatalf("page %s has access %+v after previous access %+v and must use keys %v at a higher generation", page.ID, page.Access, previous.Access, expected)
		}
	}
	requirePagesPreserved(t, before, after)

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
