package integration_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

const (
	// rollbackCorpusNodes sets the count of matching nodes that require
	// several search pages and keep a session open.
	rollbackCorpusNodes = 60
	// rollbackWaitSlices sets the worker slices a test runs while a rollout
	// waits for an open session.
	rollbackWaitSlices = 20
)

// TestSearchAccessRollbackDuringBackfill requires a node created during
// backfill to store both versions. Rollback must restore the stable
// version at once and keep every node searchable. The rollout must finish
// with only the stable version on every page and preserve document IDs,
// text, and semantic fields. The command must record its intent and OK
// outcome.
func TestSearchAccessRollbackDuringBackfill(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	entryID := rolloutEntryPoint(t, fixture, workspace)
	kind := putRolloutKind(t, fixture, workspace.OrgID)
	query := "backfill rollback lantern"
	existing := putRolloutCorpus(t, fixture, kind, entryID, query, 2)
	drainRolloutWork(t, fixture)
	before := rolloutPages(t, fixture, existing)

	begun := beginAccessRollout(t, fixture, workspace.OrgID)
	written := putRolloutNode(t, fixture, kind, entryID, "Backfill write", query+" harbor")
	drainLiveWork(t, fixture)
	requirePageVersions(t, rolloutPages(t, fixture, []uuid.UUID{written}), bothAccessVersions)
	requireAccessRollout(t, currentAccessRollout(t, fixture, workspace.OrgID), searchaccess.StableVersion, bothAccessVersions, searchdomain.AccessBackfill)

	operatorID, err := runAccessRollback(t, fixture, workspace.OrgID)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	requireRollbackAudit(t, fixture, operatorID, audit.OutcomeOK)
	rolledBack := currentAccessRollout(t, fixture, workspace.OrgID)
	requireAccessRollout(t, rolledBack, searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessVerifying)
	repeatedOperatorID, err := runAccessRollback(t, fixture, workspace.OrgID)
	if err != nil {
		t.Fatalf("Repeating the rollback failed: %v", err)
	}
	requireRollbackAudit(t, fixture, repeatedOperatorID, audit.OutcomeOK)
	if repeated := currentAccessRollout(t, fixture, workspace.OrgID); repeated.Generation != rolledBack.Generation || !slices.Equal(repeated.WriteVersions, rolledBack.WriteVersions) {
		t.Fatalf("Repeated rollback state %+v must keep the generation and write versions of the first rollback state %+v", repeated, rolledBack)
	}
	if rolledBack.PreviousVersion != searchaccess.RotatedVersion || rolledBack.Generation <= begun.Generation {
		t.Fatalf("rollback state %+v must record the rotated version as previous and a generation above %d", rolledBack, begun.Generation)
	}
	found := traverseSearch(t, fixture, workspace, query, searchAs(t, fixture, workspace, query, ""))
	for _, nodeID := range append(slices.Clone(existing), written) {
		if !slices.Contains(found, nodeID) {
			t.Fatalf("search results %v do not include node %s", found, nodeID)
		}
	}

	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessStable)
	after := rolloutPages(t, fixture, existing)
	requirePagesPreserved(t, before, after)
	requirePageVersions(t, after, []string{searchaccess.StableVersion})
	requirePageVersions(t, rolloutPages(t, fixture, []uuid.UUID{written}), []string{searchaccess.StableVersion})
}

// TestSearchAccessRollbackAfterActivation activates the rotated version,
// opens a session under it, and rolls back. New sessions must use the
// stable version and find every node. The rollout must wait with both
// versions on every page while the rotated session remains open. That
// session must finish before retirement leaves only the stable version
// and preserves document IDs, text, and semantic fields.
func TestSearchAccessRollbackAfterActivation(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	entryID := rolloutEntryPoint(t, fixture, workspace)
	query := "activated rollback orchard"
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, workspace.OrgID), entryID, query, rollbackCorpusNodes)
	drainRolloutWork(t, fixture)
	before := rolloutPages(t, fixture, nodes)

	beginAccessRollout(t, fixture, workspace.OrgID)
	activated := runRolloutUntil(t, fixture, workspace.OrgID, searchdomain.AccessRollout.Activated)
	requireAccessRollout(t, activated, searchaccess.RotatedVersion, bothAccessVersions, searchdomain.AccessVerifying)
	candidatePage := searchAs(t, fixture, workspace, query, "")
	if candidatePage.complete {
		t.Fatalf("search for %q completed in one page instead of leaving a session open", query)
	}
	if version := sessionAccessVersion(t, fixture, candidatePage.cursor); version != searchaccess.RotatedVersion {
		t.Fatalf("session uses access version %s instead of %s", version, searchaccess.RotatedVersion)
	}

	operatorID, err := runAccessRollback(t, fixture, workspace.OrgID)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	requireRollbackAudit(t, fixture, operatorID, audit.OutcomeOK)
	requireAccessRollout(t, currentAccessRollout(t, fixture, workspace.OrgID), searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessVerifying)
	restoredPage := searchAs(t, fixture, workspace, query, "")
	if version := sessionAccessVersion(t, fixture, restoredPage.cursor); version != searchaccess.StableVersion {
		t.Fatalf("session uses access version %s instead of %s", version, searchaccess.StableVersion)
	}
	requireNodesFound(t, traverseSearch(t, fixture, workspace, query, restoredPage), nodes)

	for range rollbackWaitSlices {
		runRolloutSlice(t, fixture.worker)
	}
	requireAccessRollout(t, currentAccessRollout(t, fixture, workspace.OrgID), searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessVerifying)
	requirePageVersions(t, rolloutPages(t, fixture, nodes), bothAccessVersions)
	requireNodesFound(t, traverseSearch(t, fixture, workspace, query, candidatePage), nodes)

	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.StableVersion, []string{searchaccess.StableVersion}, searchdomain.AccessStable)
	after := rolloutPages(t, fixture, nodes)
	requirePagesPreserved(t, before, after)
	requirePageVersions(t, after, []string{searchaccess.StableVersion})
}

// TestSearchAccessRollbackRefusedAfterRetirement requires rollback to
// return [searchdomain.ErrNoRolloutInProgress] for a stable authority and
// [searchdomain.ErrRetirementStarted] after retirement begins. The
// refused command must record an error outcome, preserve the rollout
// state, and allow the rollout to finish under the rotated version.
func TestSearchAccessRollbackRefusedAfterRetirement(t *testing.T) {
	fixture := newRolloutFixture(t)
	workspace := fixture.workspaces[0]
	entryID := rolloutEntryPoint(t, fixture, workspace)
	nodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, workspace.OrgID), entryID, "retired rollback compass", 1)
	drainRolloutWork(t, fixture)
	if _, err := runAccessRollback(t, fixture, workspace.OrgID); !errors.Is(err, searchdomain.ErrNoRolloutInProgress) {
		t.Fatalf("rollback returned error %v instead of %v", err, searchdomain.ErrNoRolloutInProgress)
	}

	beginAccessRollout(t, fixture, workspace.OrgID)
	retiring := runRolloutUntil(t, fixture, workspace.OrgID, func(rollout searchdomain.AccessRollout) bool {
		return rollout.Phase == searchdomain.AccessRetiring
	})
	operatorID, err := runAccessRollback(t, fixture, workspace.OrgID)
	if !errors.Is(err, searchdomain.ErrRetirementStarted) {
		t.Fatalf("rollback returned error %v instead of %v", err, searchdomain.ErrRetirementStarted)
	}
	requireRollbackAudit(t, fixture, operatorID, audit.OutcomeError)
	refused := currentAccessRollout(t, fixture, workspace.OrgID)
	if refused.ActiveVersion != searchaccess.RotatedVersion || refused.Generation != retiring.Generation ||
		!slices.Equal(refused.WriteVersions, []string{searchaccess.RotatedVersion}) {
		t.Fatalf("refused rollback changed state to %+v from %+v", refused, retiring)
	}

	final := runRolloutUntil(t, fixture, workspace.OrgID, rolloutStable)
	requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	requirePageVersions(t, rolloutPages(t, fixture, nodes), []string{searchaccess.RotatedVersion})
}

// requireNodesFound requires found to contain every node.
func requireNodesFound(t *testing.T, found, nodes []uuid.UUID) {
	t.Helper()
	for _, nodeID := range nodes {
		if !slices.Contains(found, nodeID) {
			t.Fatalf("search results %v do not include node %s", found, nodeID)
		}
	}
}
