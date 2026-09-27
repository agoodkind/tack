package integration

import (
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// restoreMatchingNodes exceeds one public page of 25 nodes. The first
// search then returns a continuation cursor.
const restoreMatchingNodes = 30

// TestSearchRestoreRejectsCursors opens a search session, then runs the
// replacement that follows a FoundationDB restore. The restored replacement
// increments the restore epoch that every session binds. The earlier cursor
// must fail. The replacement must retire the earlier session before its idle
// deadline and delete the old index. A new search must use the new index,
// include current nodes, and exclude a node deleted before the replacement.
func TestSearchRestoreRejectsCursors(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodes := make([]uuid.UUID, 0, restoreMatchingNodes)
	for number := range restoreMatchingNodes {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, entry, "restore node "+strconv.Itoa(number), "jasper restore marker", readerExcludedValue))
	}
	deleted := putOpaqueNode(t, fixture, kind, entry, "restore deleted node", "jasper restore marker", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 4000)
	first := callSearch(t, fixture.Harness, "jasper restore marker", "")
	if first.Cursor == "" {
		t.Fatal("the first page returned no continuation cursor")
	}
	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	rebuild := beginRebuild(t, fixture, searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: true, Reason: "test",
	})
	runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildRetiring
	})
	requireServing(t, fixture, rebuild.TargetIndex, "")
	if _, err := trySearch(fixture.Harness, "jasper restore marker", first.Cursor); err == nil {
		t.Fatal("a cursor from before the restored replacement continued")
	}
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	drainSearchWork(t, fixture.Worker, 4000)
	requireDeletedAbsent(t, fixture, rebuild.TargetIndex, deleted)
	results := callEverySearchPage(t, "jasper restore marker", fixture.Harness)
	requireExactlyOnce(t, results.IDs, nodes)
	if slices.Contains(results.IDs, deleted) {
		t.Fatalf("search after the restored replacement returned deleted node %s", deleted)
	}
}
