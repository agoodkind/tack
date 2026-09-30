package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchSplitDuringChanges requires each split to finish creation with
// the node scan complete, keep an unchanged node's semantic fields, and serve
// every current node. It splits the serving index from one primary shard to
// two, then from two to four, then from four to eight. It edits nodes while
// claims are paused.
func TestSearchSplitDuringChanges(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	unchanged := putOpaqueNode(t, fixture, kind, entry, "unchanged split node", "cobalt split steady", readerExcludedValue)
	edited := putOpaqueNode(t, fixture, kind, entry, "edited split node", "cobalt split original", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	before := pagesOf(t, fixture, []uuid.UUID{unchanged})
	for _, rejected := range []int{1, 3, 16} {
		if _, err := fixture.Adapter.ValidateSplit(t.Context(), fixture.Index, rejected); err == nil {
			t.Fatalf("split to %d primary shards was accepted", rejected)
		}
	}

	source := fixture.Index
	created := uuid.Nil
	for round, primaries := range []int{2, 4, 8} {
		if _, err := fixture.Adapter.ValidateSplit(t.Context(), source, primaries); err != nil {
			t.Fatalf("split to %d primary shards was rejected: %v", primaries, err)
		}
		rebuild := beginRebuild(t, fixture, searchdomain.BeginRebuild{
			Mode: searchdomain.ReplacementSplit, PrimaryShards: primaries, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test",
		})
		runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
			return found && current.Paused && current.State == searchdomain.RebuildCreating
		})
		if round == 0 {
			created = putOpaqueNode(t, fixture, kind, entry, "created split node", "cobalt split created", readerExcludedValue)
			editOpaqueNode(t, fixture, kind, edited, "cobalt split revised")
		}
		copying := runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
			return found && current.State != searchdomain.RebuildCreating
		})
		if copying.State == searchdomain.RebuildFailed || !copying.ScanComplete {
			t.Fatalf("split to %d primary shards entered %s with scan complete %t: %s", primaries, copying.State, copying.ScanComplete, copying.Failure)
		}
		runRebuildUntil(t, fixture, rebuildFinished)
		requireServing(t, fixture, rebuild.TargetIndex, source)
		settings, err := fixture.Adapter.IndexSettings(t.Context(), rebuild.TargetIndex)
		if err != nil || settings.Primaries != primaries {
			t.Fatalf("split target primaries = %d, error = %v, want %d", settings.Primaries, err, primaries)
		}
		requireSemanticPreserved(t, before, pagesIn(t, fixture, rebuild.TargetIndex, []uuid.UUID{unchanged}))
		source = rebuild.TargetIndex
	}
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, source, []uuid.UUID{unchanged, edited, created})
	results := callEverySearchPage(t, "cobalt split", fixture.Harness)
	for _, nodeID := range []uuid.UUID{unchanged, edited, created} {
		if !slices.Contains(results.IDs, nodeID) {
			t.Fatalf("search after the splits returned %v, want node %s", results.IDs, nodeID)
		}
	}
}

// TestSearchSplitWithoutModel requires native splitting to preserve semantic
// fields while inference is unavailable throughout the reserved split path.
func TestSearchSplitWithoutModel(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodeID := putOpaqueNode(t, fixture, kind, entry, "Model-independent split", "cobalt split preserved", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	before := pagesOf(t, fixture, []uuid.UUID{nodeID})
	model, err := fixture.Adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("read split model: %v", err)
	}
	undeployNativeModel(t, fixture.Adapter, fixture.Client, model.ID)
	source := fixture.Index
	for _, primaries := range []int{2, 4, 8} {
		rebuild := beginRebuild(t, fixture, searchdomain.BeginRebuild{
			Mode: searchdomain.ReplacementSplit, PrimaryShards: primaries,
			RoutingShards: 24, Replicas: 0, Restored: false, Reason: "model unavailable",
		})
		runRebuildUntil(t, fixture, rebuildFinished)
		if state := nativeModelState(t, fixture.Client, model.ID); !slices.Contains(undeployedModelStates, state) {
			t.Fatalf("split to %d primaries deployed the model: %s", primaries, state)
		}
		requireServing(t, fixture, rebuild.TargetIndex, source)
		requireSemanticPreserved(t, before, pagesIn(t, fixture, rebuild.TargetIndex, []uuid.UUID{nodeID}))
		source = rebuild.TargetIndex
	}
	if err := redeployNativeModel(t.Context(), fixture.Adapter, fixture.Client); err != nil {
		t.Fatalf("redeploy after native splits: %v", err)
	}
	results := callEverySearchPage(t, "cobalt split preserved", fixture.Harness)
	if !slices.Contains(results.IDs, nodeID) {
		t.Fatalf("search after model redeploy returned %v, want %s", results.IDs, nodeID)
	}
}
