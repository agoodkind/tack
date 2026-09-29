package integration

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchAccessVersionWithoutRebuild requires an access rollout to the
// rotated policy version to keep the physical index, alias, page text, and
// semantic fields unchanged. The rollout runs with the model undeployed.
// Every page must store only the new version. After the model is deployed
// again, search must return the nodes under the new version.
func TestSearchAccessVersionWithoutRebuild(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodes := []uuid.UUID{
		putOpaqueNode(t, fixture, kind, entry, "rotated first", "rotated key orchard", readerExcludedValue),
		putOpaqueNode(t, fixture, kind, entry, "rotated second", "rotated key orchard harbor", readerExcludedValue),
	}
	drainSearchWork(t, fixture.Worker, 2000)
	before := pagesOf(t, fixture, nodes)
	model, err := fixture.Adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("read pinned model: %v", err)
	}
	undeployNativeModel(t, fixture.Adapter, fixture.Client, model.ID)

	beginRollout(t, fixture, workspace.OrgID, searchaccess.RotatedVersion)
	final := runRolloutUntilStable(t, fixture, fixture.Worker, workspace.OrgID, func(searchdomain.AccessPhase) {})
	if final.ActiveVersion != searchaccess.RotatedVersion || !slices.Equal(final.WriteVersions, []string{searchaccess.RotatedVersion}) {
		t.Fatalf("finished rollout = %+v, want %s active and written alone", final, searchaccess.RotatedVersion)
	}
	requireAliasTarget(t, fixture)
	after := pagesOf(t, fixture, nodes)
	requireSemanticPreserved(t, before, after)
	requireAccessVersions(t, after, []string{searchaccess.RotatedVersion})

	if err := redeployNativeModel(t.Context(), fixture.Adapter, fixture.Client); err != nil {
		t.Fatalf("redeploy pinned model: %v", err)
	}
	results := callEverySearchPage(t, "rotated key orchard", fixture.Harness)
	for _, nodeID := range nodes {
		if !slices.Contains(results.IDs, nodeID) {
			t.Fatalf("search under the rotated version returned %v, want node %s", results.IDs, nodeID)
		}
	}
}

// TestSearchAccessMembershipQueryOnly requires search to refuse a caller
// after the caller's membership is removed. The membership change must leave
// every page document unchanged and schedule no search work.
func TestSearchAccessMembershipQueryOnly(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodeID := putOpaqueNode(t, fixture, kind, entry, "member node", "membership lantern", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	if results := callEverySearchPage(t, "membership lantern", fixture.Harness); !slices.Contains(results.IDs, nodeID) {
		t.Fatalf("member search returned %v, want node %s", results.IDs, nodeID)
	}
	before := pagesOf(t, fixture, []uuid.UUID{nodeID})

	pool, err := pgxpool.New(t.Context(), fixture.Config.DatabaseURL)
	if err != nil {
		t.Fatalf("open membership pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.NewOrgMemberRepo(pool).RemoveMember(t.Context(), workspace.OrgID, workspace.Actors[0].UserID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}
	if _, err := trySearch(fixture.Harness, "membership lantern", ""); err == nil {
		t.Fatal("search succeeded after the caller lost membership")
	}
	store := fixture.Stores.SearchWork(clock.Wall{})
	for _, class := range []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassAccess} {
		work, err := store.Claim(t.Context(), class, "membership-inspector", time.Minute)
		if !errors.Is(err, searchdomain.ErrNoWork) {
			t.Fatalf("membership change left %s work %+v, error = %v", class, work, err)
		}
	}
	after := pagesOf(t, fixture, []uuid.UUID{nodeID})
	requireSemanticPreserved(t, before, after)
	for position, page := range after {
		if page.SearchGeneration != before[position].SearchGeneration || !slices.Equal(page.Access.Keys, before[position].Access.Keys) {
			t.Fatalf("membership change rewrote page %d", position)
		}
	}
}

// TestSearchAccessResourceRefresh requires every page of a node that moves to
// another entry point in the same organization to receive new access keys at
// a higher generation. The text, revision, and semantic fields of each page
// must stay unchanged.
func TestSearchAccessResourceRefresh(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	first, second := sameOrgWorkspaces(t, fixture)
	from, to := entryPoint(t, fixture, first), entryPoint(t, fixture, second)
	kind := putOpaqueKind(t, fixture, first.OrgID)
	nodeID := putOpaqueNode(t, fixture, kind, from, "moved node", strings.Repeat("moved resource text ", 16), readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	before := pagesOf(t, fixture, []uuid.UUID{nodeID})

	moveOpaqueNode(t, fixture, kind, nodeID, from, to)
	drainSearchWork(t, fixture.Worker, 2000)
	after := pagesOf(t, fixture, []uuid.UUID{nodeID})
	requireSemanticPreserved(t, before, after)
	for position, page := range after {
		if slices.Equal(page.Access.Keys, before[position].Access.Keys) || pageGeneration(t, page) <= pageGeneration(t, before[position]) {
			t.Fatalf("page %d kept access %v at generation %s", position, page.Access.Keys, page.SearchGeneration)
		}
	}
}

// TestSearchAccessRolloutRecovery requires an access rollout to finish with
// only the candidate version on every page after a stopped process abandons
// a claimed step in each phase. The fixture worker claims each abandoned step
// after its lease expires.
func TestSearchAccessRolloutRecovery(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodes := []uuid.UUID{putOpaqueNode(t, fixture, kind, entry, "recovery node", strings.Repeat("recovery page text ", 16), readerExcludedValue)}
	drainSearchWork(t, fixture.Worker, 2000)

	beginRollout(t, fixture, workspace.OrgID, searchaccess.RotatedVersion)
	store := fixture.Stores.SearchWork(clock.Wall{})
	abandoned := make([]searchdomain.AccessPhase, 0, 4)
	final := runRolloutUntilStable(t, fixture, fixture.Worker, workspace.OrgID, func(phase searchdomain.AccessPhase) {
		if _, err := store.Claim(t.Context(), searchdomain.WorkClassRollout, "stopped-process", rolloutAbandonLease); err == nil {
			abandoned = append(abandoned, phase)
		}
	})
	if len(abandoned) < 3 {
		t.Fatalf("abandoned rollout steps in phases %v, want backfill, verifying, and retiring", abandoned)
	}
	if final.ActiveVersion != searchaccess.RotatedVersion {
		t.Fatalf("recovered rollout active version = %s", final.ActiveVersion)
	}
	requireAccessVersions(t, pagesOf(t, fixture, nodes), []string{searchaccess.RotatedVersion})
}
