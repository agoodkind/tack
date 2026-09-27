package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchRebuildDuringChanges runs a full FoundationDB replacement while
// nodes are created, edited, deleted, and moved, a definition is declared,
// and a permission version transition starts. A topology with a routing
// count that is not a multiple of the primary count must be refused. The
// test deletes the old index before retirement runs. That state matches a
// retry after an earlier attempt deleted the index, and retirement must
// still finish. After the switch the public alias and FoundationDB select
// the new index, the old index is gone, and the new index serves exactly
// the current nodes.
func TestSearchRebuildDuringChanges(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	first, second := sameOrgWorkspaces(t, fixture)
	from, to := entryPoint(t, fixture, first), entryPoint(t, fixture, second)
	kind := putOpaqueKind(t, fixture, first.OrgID)
	edited := putOpaqueNode(t, fixture, kind, from, "edited node", "amber rebuild original", readerExcludedValue)
	deleted := putOpaqueNode(t, fixture, kind, from, "deleted node", "amber rebuild removed", readerExcludedValue)
	moved := putOpaqueNode(t, fixture, kind, from, "moved node", strings.Repeat("amber rebuild moved ", 12), readerExcludedValue)
	unchanged := putOpaqueNode(t, fixture, kind, from, "unchanged node", "amber rebuild steady", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)

	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test"}
	requireTopologyRejected(t, fixture, request)
	rebuild := beginRebuild(t, fixture, request)
	requireSecondRefused(t, fixture, request)
	runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildCopying
	})

	created := putOpaqueNode(t, fixture, kind, from, "created node", "amber rebuild created", readerExcludedValue)
	editOpaqueNode(t, fixture, kind, edited, "amber rebuild revised")
	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	moveOpaqueNode(t, fixture, kind, moved, from, to)
	declared := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: kind.OrgID, Name: opaqueSearchKey("p"), Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 3, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(t.Context(), declared); err != nil {
		t.Fatalf("declare property during replacement: %v", err)
	}
	beginRollout(t, fixture, kind.OrgID, searchaccess.RotatedVersion)

	runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildRetiring
	})
	deleteNativeIndex(t, fixture.Client, fixture.Index)
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	runRolloutUntilStable(t, fixture, fixture.Worker, kind.OrgID, func(searchdomain.AccessPhase) {})
	drainSearchWork(t, fixture.Worker, 2000)

	current := []uuid.UUID{created, edited, moved, unchanged}
	requireCurrentPages(t, fixture, rebuild.TargetIndex, current)
	requireDeletedAbsent(t, fixture, rebuild.TargetIndex, deleted)
	requireAccessVersions(t, pagesIn(t, fixture, rebuild.TargetIndex, current), []string{searchaccess.RotatedVersion})
	results := callEverySearchPage(t, "amber rebuild", fixture.Harness)
	if slices.Contains(results.IDs, deleted) {
		t.Fatalf("search returned deleted node %s", deleted)
	}
	for _, nodeID := range []uuid.UUID{created, edited, unchanged} {
		if !slices.Contains(results.IDs, nodeID) {
			t.Fatalf("search after the replacement returned %v, want node %s", results.IDs, nodeID)
		}
	}
}
