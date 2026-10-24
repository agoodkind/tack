package integration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchRebuildRecoversFromStepFailures runs one full FoundationDB
// replacement while nodes are created, edited, and deleted. The ML Commons
// memory circuit breaker first refuses every prediction while copies and
// live work write pages. [failReplayValidationSwitch] then fails replay,
// validation, and alias switching. During each failure the public alias and
// FoundationDB must select the source. After each condition clears, the
// replacement must resume from its durable state and finish with the target
// serving every current node.
func TestSearchRebuildRecoversFromStepFailures(t *testing.T) {
	shortenSearchSessions(t)
	fixture := newQueryFixture(t, defaultQueryOptions())
	fixture.Worker = faultWorker(t, fixture)
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	steady := putOpaqueNode(t, fixture, kind, entry, "steady fault node", "umber fault steady", readerExcludedValue)
	removed := putOpaqueNode(t, fixture, kind, entry, "removed fault node", "umber fault removed", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	source := fixture.Index
	rebuild := beginRebuild(t, fixture, searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test",
	})
	runBoundedRebuildUntil(t, fixture, source, inState(searchdomain.RebuildCopying))

	t.Cleanup(func() {
		if err := setBreakerThreshold(context.Background(), fixture, "null"); err != nil {
			t.Errorf("restore the breaker threshold: %v", err)
		}
	})
	requireBreakerThreshold(t, fixture, "1")
	created := putOpaqueNode(t, fixture, kind, entry, "created fault node", "umber fault created", readerExcludedValue)
	runFaultedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, _ bool, failures []error) bool {
		return current.ScanComplete && failedWith(failures, "write search page")
	})
	requireServing(t, fixture, source, "")
	requireQueued(t, fixture, rebuild.TargetIndex, steady, created)
	requireBreakerThreshold(t, fixture, "null")

	if err := fixture.Stores.Nodes.Delete(WithTestOperation(t, t.Context()), kind.OrgID, removed); err != nil {
		t.Fatalf("delete node during the replacement: %v", err)
	}
	edited := putOpaqueNode(t, fixture, kind, entry, "edited fault node", "umber fault original", readerExcludedValue)
	editOpaqueNode(t, fixture, kind, edited, "umber fault revised")
	failReplayValidationSwitch(t, fixture, rebuild, edited, steady, "umber fault")
	requireServing(t, fixture, rebuild.TargetIndex, source)
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, rebuild.TargetIndex, []uuid.UUID{steady, edited, created})
	requireDeletedAbsent(t, fixture, rebuild.TargetIndex, removed)
	if results := requireSearchFinds(t, fixture, "umber fault", steady, edited, created); slices.Contains(results, removed) {
		t.Fatalf("search returned deleted node %s", removed)
	}
}

// failReplayValidationSwitch fails three steps of rebuild in turn with real
// engine conditions and runs the replacement to its end:
//
//  1. Replay: the target blocks writes while live work for replayed mirrors
//     its pages.
//  2. Validation: verification finds the pages of verified missing from the
//     target.
//  3. Alias switching: the read-only target refuses the alias change.
//
// Before each condition clears, the public alias must select the source,
// and public search for query must read verified from it.
func failReplayValidationSwitch(t *testing.T, fixture queryFixture, rebuild searchdomain.Rebuild, replayed, verified uuid.UUID, query string) {
	t.Helper()
	source, target := rebuild.SourceIndex, rebuild.TargetIndex
	setIndexBlock(t, fixture.Client, target, writeBlockSetting, true)
	runFaultedRebuildUntil(t, fixture, source, func(_ searchdomain.Rebuild, _ bool, failures []error) bool {
		return failedWith(failures, replayed.String()+" at generation") && pagesCurrent(t, fixture, source, replayed)
	})
	requireServing(t, fixture, source, "")
	requireWritable(t, fixture.Client, source)
	requireQueued(t, fixture, target, replayed)
	clearIndexBlock(t, fixture.Client, target, writeBlockSetting)
	if started := runBoundedRebuildUntil(t, fixture, source, inState(searchdomain.RebuildVerifying)); started.VerifyCursor != "" {
		t.Fatalf("verification advanced to cursor %q before the test removed pages", started.VerifyCursor)
	}
	deleteNodePages(t, fixture.Client, target, verified)
	runBoundedRebuildUntil(t, fixture, source, inState(searchdomain.RebuildSwitching))
	if !pagesCurrent(t, fixture, target, verified) {
		t.Fatalf("verification switched without restoring the pages of node %s", verified)
	}

	setIndexBlock(t, fixture.Client, target, readOnlySetting, true)
	runFaultedRebuildUntil(t, fixture, source, func(_ searchdomain.Rebuild, _ bool, failures []error) bool {
		return failedWith(failures, "switch public alias")
	})
	requireServing(t, fixture, source, "")
	requireSearchFinds(t, fixture, query, verified)
	clearIndexBlock(t, fixture.Client, target, readOnlySetting)
	runBoundedRebuildUntil(t, fixture, source, rebuildFinished)
}

// deleteNodePages deletes every document of nodeID from index.
func deleteNodePages(t *testing.T, client *opensearchapi.Client, index string, nodeID uuid.UUID) {
	t.Helper()
	refresh := true
	query := strings.NewReader(fmt.Sprintf(`{"query":{"term":{"node_id":%q}}}`, nodeID.String()))
	response, err := client.Document.DeleteByQuery(WithTestOperation(t, t.Context()), opensearchapi.DocumentDeleteByQueryReq{
		Indices: []string{index}, Body: query, Params: opensearchapi.DocumentDeleteByQueryParams{Refresh: &refresh},
	})
	if err != nil || response.Deleted == 0 {
		t.Fatalf("delete pages of node %s from %s: response %+v, error %v", nodeID, index, response, err)
	}
}

// TestSearchRebuildDuringChanges runs a full FoundationDB replacement while
// nodes are created, edited, deleted, and moved, a definition is declared,
// and a permission version transition starts. A topology with a routing
// count that is not a multiple of the primary count must be refused. The
// test deletes the old index before retirement runs. That state matches a
// retry after an earlier attempt deleted the index, and retirement must
// still finish. After the switch the public alias and FoundationDB select
// the new index, the old index is gone, and the new index serves exactly
// the current nodes. The refused topology must leave the source writable.
// Before every worker slice, the test indexes must be at most one serving,
// one replacement, and one retiring index.
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
	requireWritable(t, fixture.Client, fixture.Index)
	rebuild := beginRebuild(t, fixture, request)
	requireSecondRefused(t, fixture, request)
	runBoundedRebuildUntil(t, fixture, fixture.Index, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildCopying
	})

	created := putOpaqueNode(t, fixture, kind, from, "created node", "amber rebuild created", readerExcludedValue)
	editOpaqueNode(t, fixture, kind, edited, "amber rebuild revised")
	if err := fixture.Stores.Nodes.Delete(WithTestOperation(t, t.Context()), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	moveOpaqueNode(t, fixture, kind, moved, from, to)
	declared := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: kind.OrgID, Name: opaqueSearchKey("p"), Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 3, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(WithTestOperation(t, t.Context()), declared); err != nil {
		t.Fatalf("declare property during replacement: %v", err)
	}
	beginRollout(t, fixture, kind.OrgID, searchaccess.RotatedVersion)

	runBoundedRebuildUntil(t, fixture, fixture.Index, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildRetiring
	})
	deleteNativeIndex(t, fixture.Client, fixture.Index)
	runBoundedRebuildUntil(t, fixture, fixture.Index, rebuildFinished)
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
