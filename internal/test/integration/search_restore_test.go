package integration

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// restoreMatchingNodes exceeds one public page of 25 nodes. The first
// search then returns a continuation cursor.
const restoreMatchingNodes = 30

// TestSearchRestoreRejectsCursors restores a completed FoundationDB backup
// into a fresh cluster. A full replacement rejects the saved cursor and
// searches restored nodes, final pages, and the approved relevance corpus.
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
	longNode := putOpaqueNode(t, fixture, kind, entry, "restore final page", strings.Repeat("jasper restore marker ", 24)+"final restored unicode 汉字", readerExcludedValue)
	nodes = append(nodes, longNode)
	targets := make(map[string]uuid.UUID)
	for _, item := range semanticCorpus(t) {
		targets[item.Identifier] = putOpaqueNode(t, fixture, kind, entry, item.Text, item.Text, readerExcludedValue)
	}
	drainSearchWork(t, fixture.Worker, 4000)
	first := callSearch(t, fixture.Harness, "jasper restore marker", "")
	if first.Cursor == "" {
		t.Fatal("the first page returned no continuation cursor")
	}
	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	fixture.Graph.Close()
	backup := testenv.BackupFoundationDB(t)
	postBackup := putOpaqueNode(t, fixture, kind, entry, "created after snapshot", "jasper restore marker", readerExcludedValue)
	editOpaqueNode(t, fixture, kind, nodes[0], "postbackup changed text")
	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, nodes[1]); err != nil {
		t.Fatalf("delete after snapshot: %v", err)
	}
	restoreQueryFixture(t, &fixture, backup.Restore(t))
	requireRestoredSnapshot(t, fixture, kind.OrgID, nodes[0], nodes[1], postBackup)

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
	requireCurrentPages(t, fixture, rebuild.TargetIndex, nodes)
	results := callEverySearchPage(t, "jasper restore marker", fixture.Harness)
	requireExactlyOnce(t, results.IDs, nodes)
	if slices.Contains(results.IDs, deleted) {
		t.Fatalf("search after the restored replacement returned deleted node %s", deleted)
	}
	finalPage := callSearch(t, fixture.Harness, "final restored unicode 汉字", "")
	if !slices.Contains(finalPage.IDs, longNode) {
		t.Fatalf("restored final-page query returned %v, want node %s", finalPage.IDs, longNode)
	}
	fixture.Index = rebuild.TargetIndex
	requireSemanticTargets(t, fixture, semanticPairs(t), targets, "restored")
}
