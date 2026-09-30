package integration

import (
	"bytes"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
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
	nodes := make([]uuid.UUID, 0, 30)
	for number := range 30 {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, entry, "Split node "+strconv.Itoa(number), "cobalt split preserved", readerExcludedValue))
	}
	relevanceKind := putOpaqueKind(t, fixture, workspace.OrgID)
	targets := make(map[string]uuid.UUID)
	allNodes := slices.Clone(nodes)
	for _, item := range semanticCorpus(t) {
		id := putOpaqueNode(t, fixture, relevanceKind, entry, item.Text, item.Text, readerExcludedValue)
		targets[item.Identifier] = id
		allNodes = append(allNodes, id)
	}
	drainSearchWork(t, fixture.Worker, 5000)
	before := pagesOf(t, fixture, allNodes)
	mapping := splitMapping(t, fixture, fixture.Index)
	query := searchdomain.Query{Text: "cobalt split preserved", Index: fixture.Index, NodeType: kind.TypeKey, Access: callerAccess(t, fixture, workspace, entry)}
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	snapshot, err := ranker.Open(t.Context(), query)
	if err != nil {
		t.Fatalf("predict split control: %v", err)
	}
	tokens := bytes.Clone(snapshot.QueryTokens)
	if err := ranker.Close(t.Context(), snapshot); err != nil {
		t.Fatalf("close split control: %v", err)
	}
	rawNodes := savedSparseNodes(t, fixture, query, tokens)
	requireExactlyOnce(t, rawNodes, nodes)
	model, err := fixture.Adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("read split model: %v", err)
	}
	defer func() {
		if t.Failed() {
			captureSearchFailure(t, fixture, model.ID, testenv.OpenSearch(t).Container)
		}
	}()
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
		requireSemanticPreserved(t, before, pagesIn(t, fixture, rebuild.TargetIndex, allNodes))
		if actual := splitMapping(t, fixture, rebuild.TargetIndex); actual != mapping {
			t.Fatalf("split to %d changed its mapping", primaries)
		}
		query.Index = rebuild.TargetIndex
		if actual := savedSparseNodes(t, fixture, query, tokens); !slices.Equal(actual, rawNodes) {
			t.Fatalf("split to %d changed saved sparse results: %v", primaries, actual)
		}
		t.Logf("model-free split primaries=%d nodes=%d sparse_nodes=%d token_bytes=%d", primaries, len(allNodes), len(rawNodes), len(tokens))
		source = rebuild.TargetIndex
	}
	if err := redeployNativeModel(t.Context(), fixture.Adapter, fixture.Client); err != nil {
		t.Fatalf("redeploy after native splits: %v", err)
	}
	fixture.Index = source
	requireExactlyOnce(t, everyTypedSearchPage(t, fixture, "cobalt split preserved", kind.TypeKey), nodes)
	requireSemanticTargets(t, fixture, semanticPairs(t), targets, "split-redeployed")
}
