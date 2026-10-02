package integration

import (
	"errors"
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
	// floodStageMemoryBytes is the 8 GiB model floor plus the data tmpfs,
	// which the container's memory cgroup is charged for.
	floodStageMemoryBytes int64 = 11 << 30
	// floodStageDataBytes bounds the data path. ML Commons writes the model
	// files there, beside the index and the filler.
	floodStageDataBytes int64 = 3 << 30
	// floodStageFillPercent is above the 95 percent flood-stage default of
	// OpenSearch 3.8.0 and below a full disk.
	floodStageFillPercent = 97
	// floodStageBlockError is the bulk item error of a write to an index with
	// the read_only_allow_delete block.
	floodStageBlockError = "cluster_block_exception"
	// floodStageFailures is one more than the attempt limit of live work. A
	// counted failure excludes the node at the limit.
	floodStageFailures = 6
	floodStageOldText  = "amber flood marker"
	floodStageNewText  = "violet flood marker"
)

// TestSearchClusterDiskFloodStage fills the data path of a disposable engine
// past the flood-stage watermark. OpenSearch marks the serving index
// read_only_allow_delete on its own. Source writes still commit, search
// still answers from the index, and the blocked search work stays pending
// without counting toward exclusion. After the filler is removed, OpenSearch
// releases the block on its own, and the pending work makes the edits
// searchable. No step changes a watermark or a block setting.
func TestSearchClusterDiskFloodStage(t *testing.T) {
	engine := testenv.DisposableOpenSearch(t, testenv.DisposableOpenSearchOptions{
		MemoryBytes: floodStageMemoryBytes, DataBytes: floodStageDataBytes,
	})
	fixture, _ := newClusterQueryFixture(t, engine.Fixture)
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	parent := entryPoint(t, fixture, workspace)
	nodeA := putOpaqueNode(t, fixture, kind, parent, "flood node A", floodStageOldText, readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 500)
	requireQueryResults(t, fixture, floodStageOldText, []uuid.UUID{nodeA}, nil)

	size, used := engine.FillData(t, floodStageFillPercent)
	t.Logf("data path after the fill: %d of %d bytes used", used, size)
	waitForBlock(t, fixture, true)

	editOpaqueNode(t, fixture, kind, nodeA, floodStageNewText)
	nodeB := putOpaqueNode(t, fixture, kind, parent, "flood node B", floodStageNewText, readerExcludedValue)
	requireIncludedText(t, fixture, kind, nodeA, floodStageNewText)
	requireIncludedText(t, fixture, kind, nodeB, floodStageNewText)
	requireBlockedFailures(t, fixture, nodeB)
	requireLiveWorkPending(t, fixture, nodeB)
	report, err := runSearchVerifyCommand(t, fixture.Config)
	if report.ExcludedNodes != 0 || report.StuckWorkItems != 0 {
		t.Fatalf("ops search verify during the block = %+v (error %v), want no excluded node and no stuck work", report, err)
	}
	requireQueryResults(t, fixture, floodStageOldText, []uuid.UUID{nodeA}, nil)
	requireQueryResults(t, fixture, floodStageNewText, nil, []uuid.UUID{nodeA, nodeB})

	size, used = engine.FreeData(t)
	t.Logf("data path after the release: %d of %d bytes used", used, size)
	waitForBlock(t, fixture, false)
	clusterEventually(t, "index the pending edits", func() error {
		drainSearchWork(t, fixture.Worker, 500)
		return queryResultsMatch(t, fixture, floodStageNewText, []uuid.UUID{nodeA, nodeB}, nil)
	})
	requireQueryResults(t, fixture, floodStageOldText, nil, []uuid.UUID{nodeA})
}

// requireBlockedFailures runs worker slices until floodStageFailures slices
// have failed for nodeID with the block error. Each failure waits out the
// retry delay of released work.
func requireBlockedFailures(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	failures := 0
	clusterEventually(t, "fail the node's blocked search work repeatedly", func() error {
		_, err := fixture.Worker.RunSlice(t.Context())
		if err != nil && strings.Contains(err.Error(), nodeID.String()) {
			if !strings.Contains(err.Error(), floodStageBlockError) {
				t.Fatalf("worker slice for node %s failed without %s: %v", nodeID, floodStageBlockError, err)
			}
			failures++
		}
		if failures < floodStageFailures {
			return fmt.Errorf("%d of %d blocked failures for node %s", failures, floodStageFailures, nodeID)
		}
		return nil
	})
}

// requireLiveWorkPending claims nodeID's live work and yields it.
func requireLiveWorkPending(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	store := fixture.Stores.SearchWork(clock.Wall{})
	clusterEventually(t, "claim the node's pending live work", func() error {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, "flood-inspector", time.Minute)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return clusterFailure("claim live work", err)
		}
		clusterRequire(t, "claim live work", err)
		clusterRequire(t, "yield inspected work", store.Yield(t.Context(), work))
		if work.NodeID != nodeID {
			return fmt.Errorf("claimed live work of node %s", work.NodeID)
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
