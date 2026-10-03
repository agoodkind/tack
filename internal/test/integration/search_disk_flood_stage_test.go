package integration

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

const (
	// floodStageMemoryBytes is the 8 GiB of memory that the search model
	// needs. The data path is on the disk of the Docker VM.
	floodStageMemoryBytes int64 = 8 << 30
	// floodStageDataBytes is the size of the data path. ML Commons writes the
	// model files there. At the fill percent, about 1.25 GiB stays free. That
	// is more than the 1 GiB ML Commons threshold. Model writes and queries
	// still run during the block.
	floodStageDataBytes int64 = 32 << 30
	// floodStageFillPercent is above the 95 percent flood-stage default of
	// OpenSearch 3.8.0 and leaves more free space than the ML Commons disk
	// threshold.
	floodStageFillPercent = 96
	// floodStageReleasePercent is the 90 percent high watermark. The disk
	// threshold monitor keeps the block on an index above it.
	floodStageReleasePercent = 90
	// floodStageBlockError and floodStageBlockStatus identify the bulk item
	// result of a write to an index with the read_only_allow_delete block.
	floodStageBlockError  = "cluster_block_exception"
	floodStageBlockStatus = "status 429"
	// floodStageFailures equals the attempt limit of live work. A counted
	// failure excludes the node at the limit, and an uncounted one does not.
	floodStageFailures = 5
	floodStageOldText  = "amber lantern quarry"
	floodStageNewText  = "violet harbor orchid"
)

// TestSearchClusterDiskFloodStage fills the data path of a disposable engine
// past the flood-stage watermark. OpenSearch marks the serving index
// read_only_allow_delete on its own. MCP writes still commit, search still
// answers from the index, and the blocked search work stays pending without
// counting toward exclusion. After the filler is removed, OpenSearch releases
// the block on its own, and the pending work makes the edits searchable. No
// step changes a watermark or a block setting.
func TestSearchClusterDiskFloodStage(t *testing.T) {
	engine := testenv.DisposableOpenSearch(t, testenv.DisposableOpenSearchOptions{
		MemoryBytes: floodStageMemoryBytes, DataBytes: floodStageDataBytes,
	})
	fixture, _ := newClusterQueryFixture(t, engine.Fixture)
	requireDiskSettings(t, fixture)
	nodeA := createFloodProject(t, fixture, "FLDA", floodStageOldText)
	drainSearchWork(t, fixture.Worker, 500)
	requireQueryResults(t, fixture, floodStageOldText, []uuid.UUID{nodeA}, nil)
	clusterRequire(t, "refresh before the fill", refreshServingIndex(t, fixture))
	logStoredPages(t, fixture, nodeA, "before the fill")

	size, used := engine.FillData(t, floodStageFillPercent)
	t.Logf("data path after the fill: %d of %d bytes used; cgroup memory.current %s", used, size, engine.MemoryCurrent(t))
	waitForBlock(t, fixture, true)

	renameFloodProject(t, fixture, nodeA, floodStageNewText)
	nodeB := createFloodProject(t, fixture, "FLDB", floodStageNewText+" beacon")
	requireStoredName(t, fixture, nodeA, floodStageNewText)
	requireStoredName(t, fixture, nodeB, floodStageNewText+" beacon")
	requireBlockedFailures(t, fixture, nodeB)
	// Verify runs before the pending-work check. A counted failure at the
	// attempt limit excludes node B and removes its live work, and verify
	// then reports the exclusion.
	report, err := runSearchVerifyCommand(t, fixture.Config)
	if err != nil || report.ExcludedNodes != 0 || report.StuckWorkItems != 0 {
		t.Fatalf("ops search verify during the block = %+v (error %v), want no error, no excluded node, and no stuck work", report, err)
	}
	requireLiveWorkPending(t, fixture, nodeB)
	// The sparse model relates the two texts to each other. A search result
	// does not show which text the index stores, and these checks read the
	// stored page text.
	logStoredPages(t, fixture, nodeA, "during the block")
	requireStoredText(t, fixture, nodeA, floodStageOldText, floodStageNewText)
	requireNoStoredPages(t, fixture, nodeB)

	size, used = engine.FreeData(t)
	t.Logf("data path after the free: %d of %d bytes used", used, size)
	if used*100 >= size*floodStageReleasePercent {
		t.Fatalf("data path still %d of %d bytes used after the free, at or above the %d percent high watermark", used, size, floodStageReleasePercent)
	}
	waitForBlock(t, fixture, false)
	clusterEventually(t, "index the pending edits", func() error {
		drainSearchWork(t, fixture.Worker, 500)
		if err := refreshServingIndex(t, fixture); err != nil {
			return err
		}
		if err := storedTextError(t, fixture, nodeA, floodStageNewText, floodStageOldText); err != nil {
			return err
		}
		if err := storedTextError(t, fixture, nodeB, floodStageNewText+" beacon", floodStageOldText); err != nil {
			return err
		}
		return queryResultsMatch(t, fixture, floodStageNewText, []uuid.UUID{nodeA, nodeB}, nil)
	})
}

// requireBlockedFailures runs worker slices until floodStageFailures slices
// have failed for nodeID with the 429 block error. Each failure waits out the
// retry delay of released work.
func requireBlockedFailures(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	failures := 0
	clusterEventually(t, "fail the node's blocked search work repeatedly", func() error {
		_, err := fixture.Worker.RunSlice(t.Context())
		if err != nil && strings.Contains(err.Error(), nodeID.String()) {
			if !strings.Contains(err.Error(), floodStageBlockError) || !strings.Contains(err.Error(), floodStageBlockStatus) {
				t.Fatalf("worker slice for node %s failed without %s and %s: %v", nodeID, floodStageBlockStatus, floodStageBlockError, err)
			}
			failures++
		}
		if failures < floodStageFailures {
			return fmt.Errorf("%d of %d blocked failures for node %s", failures, floodStageFailures, nodeID)
		}
		return nil
	})
}

// requireLiveWorkPending claims every claimable live item and keeps each
// claim until nodeID's item is among them, then yields each claimed item.
// An older item in the same bucket then cannot hide nodeID's item.
func requireLiveWorkPending(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	store := fixture.Stores.SearchWork(clock.Wall{})
	clusterEventually(t, "claim the node's pending live work", func() error {
		claimed := claimAllFor(t, store, searchdomain.WorkClassLive, time.Minute)
		for _, work := range claimed {
			clusterRequire(t, "yield inspected work", store.Yield(t.Context(), work))
		}
		if _, found := claimed[nodeID]; !found {
			return fmt.Errorf("claimed %d live items, none of node %s", len(claimed), nodeID)
		}
		return nil
	})
}

// requireQueryResults requires every node of present and no node of absent
// in the complete results of query.
func requireQueryResults(t *testing.T, fixture queryFixture, query string, present, absent []uuid.UUID) {
	t.Helper()
	if err := queryResultsMatch(t, fixture, query, present, absent); err != nil {
		t.Fatal(err)
	}
}

func queryResultsMatch(t *testing.T, fixture queryFixture, query string, present, absent []uuid.UUID) error {
	t.Helper()
	found, cursor := []uuid.UUID{}, ""
	for range clusterSearchPages {
		page, err := trySearch(fixture.Harness, query, cursor)
		if err != nil {
			return fmt.Errorf("search %q: %w", query, err)
		}
		found = append(found, page.IDs...)
		if page.Complete {
			break
		}
		cursor = page.Cursor
	}
	for _, nodeID := range present {
		if !slices.Contains(found, nodeID) {
			return fmt.Errorf("search %q returned %v without node %s", query, found, nodeID)
		}
	}
	for _, nodeID := range absent {
		if slices.Contains(found, nodeID) {
			return fmt.Errorf("search %q returned node %s", query, nodeID)
		}
	}
	return nil
}
