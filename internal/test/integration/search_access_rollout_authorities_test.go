package integration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// rolloutFailureSlices sets the count of refused verification reads the
// test requires before checking the active version.
const rolloutFailureSlices = 2

// TestSearchAccessRolloutTwoAuthorities runs rollouts for two
// organizations at once. Beginning one must preserve the other
// authority's state. The second organization must finish while the first
// waits after activation for a session under the stable version. Each
// rollout must keep its own generation and session presence. The open
// session must continue, and the first rollout must finish after the
// session ends.
func TestSearchAccessRolloutTwoAuthorities(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, other := fixture.workspaces[0], otherOrganizationWorkspace(t, fixture)
	query := "authority rollout lantern"
	firstNodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, first.OrgID), rolloutEntryPoint(t, fixture, first), query, rollbackCorpusNodes)
	otherNodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, other.OrgID), rolloutEntryPoint(t, fixture, other), query, 1)
	drainRolloutWork(t, fixture)

	otherBefore := currentAccessRollout(t, fixture, other.OrgID)
	firstBegun := beginAccessRollout(t, fixture, first.OrgID)
	if unchanged := currentAccessRollout(t, fixture, other.OrgID); unchanged.Generation != otherBefore.Generation || unchanged.Phase != searchdomain.AccessStable {
		t.Fatalf("beginning one rollout changed the other authority's state to %+v from %+v", unchanged, otherBefore)
	}
	otherBegun := beginAccessRollout(t, fixture, other.OrgID)
	openPage := searchAs(t, fixture, first, query, "")
	if openPage.complete || sessionAccessVersion(t, fixture, openPage.cursor) != searchaccess.StableVersion {
		t.Fatalf("the first page must leave a stable version session open but reports completion %t", openPage.complete)
	}

	runRolloutUntil(t, fixture, first.OrgID, func(searchdomain.AccessRollout) bool {
		firstState, otherState := currentAccessRollout(t, fixture, first.OrgID), currentAccessRollout(t, fixture, other.OrgID)
		return firstState.Activated() && otherState.Phase == searchdomain.AccessStable
	})
	for range rollbackWaitSlices {
		runRolloutSlice(t, fixture.worker)
	}
	firstWaiting, otherDone := currentAccessRollout(t, fixture, first.OrgID), currentAccessRollout(t, fixture, other.OrgID)
	requireAccessRollout(t, firstWaiting, searchaccess.RotatedVersion, bothAccessVersions, searchdomain.AccessVerifying)
	requireAccessRollout(t, otherDone, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	if firstWaiting.Generation != firstBegun.Generation || otherDone.Generation != otherBegun.Generation {
		t.Fatalf("rollout generations differ: first %d instead of %d, second %d instead of %d", firstWaiting.Generation, firstBegun.Generation, otherDone.Generation, otherBegun.Generation)
	}
	requireVersionSessions(t, fixture, first.OrgID, true)
	requireVersionSessions(t, fixture, other.OrgID, false)

	continued := searchAs(t, fixture, first, query, openPage.cursor)
	if len(continued.ids) == 0 {
		t.Fatal("the open session did not return further results")
	}
	requireNodesFound(t, traverseSearch(t, fixture, first, query, continued), slices.DeleteFunc(slices.Clone(firstNodes), func(nodeID uuid.UUID) bool {
		return slices.Contains(openPage.ids, nodeID)
	}))
	requireVersionSessions(t, fixture, first.OrgID, false)

	final := runRolloutUntil(t, fixture, first.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, append(slices.Clone(firstNodes), otherNodes...)), []string{searchaccess.RotatedVersion})
}

// TestSearchAccessFailureBeforeActivation refuses every verification read
// through a proxy. The rollout must keep the stable version active, and
// searches must find every node. Rollback must finish with only the
// stable version on every page and preserve document IDs, text, and
// semantic fields.
func TestSearchAccessFailureBeforeActivation(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	query := "failed rollout beacon"
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, workspace.OrgID), rolloutEntryPoint(t, fixture, workspace), query, 2)
	drainRolloutWork(t, fixture)
	before := rolloutPages(t, fixture, nodes)
	refusing := newRefusingWorker(t, fixture)

	beginAccessRollout(t, fixture, workspace.OrgID)
	runWorkerUntil(t, fixture, refusing.worker, workspace.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.Phase == searchdomain.AccessVerifying
	})
	refusing.refuse.Store(true)
	failures := 0
	deadline := time.Now().Add(rolloutStepDeadline)
	for failures < rolloutFailureSlices && time.Now().Before(deadline) {
		claimed, err := refusing.worker.RunSlice(t.Context())
		switch {
		case err != nil && strings.Contains(err.Error(), "read access rollout page documents"):
			failures++
		case err != nil:
			waitAfterRejection(t, err)
		case !claimed:
			time.Sleep(rolloutIdlePause)
		}
	}
	failed := currentAccessRollout(t, fixture, workspace.OrgID)
	if failures < rolloutFailureSlices || failed.Activated() {
		t.Fatalf("verification failed %d times and must keep the candidate inactive: %+v", failures, failed)
	}
	requireAccessRollout(t, failed, searchaccess.StableVersion, bothAccessVersions, searchdomain.AccessVerifying)
	requireNodesFound(t, traverseSearch(t, fixture, workspace, query, searchAs(t, fixture, workspace, query, "")), nodes)

	if _, err := runAccessRollback(t, fixture, workspace.OrgID); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessStable)
	after := rolloutPages(t, fixture, nodes)
	requirePagesPreserved(t, before, after)
	requirePageVersions(t, after, []string{searchaccess.StableVersion})
}

// requireVersionSessions requires authorityID's stable version session
// presence to equal want.
func requireVersionSessions(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID, want bool) {
	t.Helper()
	rollouts := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet())
	active, err := rollouts.HasActiveAccessVersion(t.Context(), authorityID, searchaccess.StableVersion)
	if err != nil || active != want {
		t.Fatalf("authority %s reports stable version session presence %t with error %v instead of presence %t", authorityID, active, err, want)
	}
}
