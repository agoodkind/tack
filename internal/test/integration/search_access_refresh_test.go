package integration

import (
	"context"
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

// TestSearchAccessVersionWithoutRebuild rolls the rotated policy version out
// with the model undeployed. The physical index, alias, page text, and
// semantic fields must not change. Every page must store only the new
// version, and search must use the new version once the model is deployed
// again.
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
	redeploy := func(ctx context.Context) error {
		_, provisionErr := fixture.Adapter.Provision(ctx)
		return provisionErr
	}
	undeployNativeModel(t, fixture.Client, model.ID, redeploy)

	beginRollout(t, fixture, workspace.OrgID, searchaccess.RotatedVersion)
	final := runRolloutUntilStable(t, fixture, fixture.Worker, workspace.OrgID, func(searchdomain.AccessPhase) {})
	if final.ActiveVersion != searchaccess.RotatedVersion || !slices.Equal(final.WriteVersions, []string{searchaccess.RotatedVersion}) {
		t.Fatalf("finished rollout = %+v, want %s active and written alone", final, searchaccess.RotatedVersion)
	}
	requireAliasTarget(t, fixture)
	after := pagesOf(t, fixture, nodes)
	requireSemanticPreserved(t, before, after)
	requireAccessVersions(t, after, []string{searchaccess.RotatedVersion})

	if err := redeploy(t.Context()); err != nil {
		t.Fatalf("redeploy pinned model: %v", err)
	}
	results := callEverySearchPage(t, "rotated key orchard", fixture.Harness)
	for _, nodeID := range nodes {
		if !slices.Contains(results.IDs, nodeID) {
			t.Fatalf("search under the rotated version returned %v, want node %s", results.IDs, nodeID)
		}
	}
}

// TestSearchAccessMembershipQueryOnly removes the caller's membership. Search
// must refuse the caller, and no page document or search work may change.
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

// TestSearchAccessResourceRefresh moves a node to another entry point of the
// same organization. Every page must receive new access keys at a higher
// generation while its text, revision, and semantic fields stay unchanged.
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

// TestSearchAccessRolloutRecovery abandons a claimed rollout step in every
// phase, as a stopped process would, and continues with a new worker. The
// rollout must finish with only the candidate version on every page.
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
