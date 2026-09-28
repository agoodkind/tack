package integration

import (
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	deleteFanoutCounterparts = 120
	deleteFanoutPageBytes    = 256
	deleteFanoutSlices       = 3000
)

// TestSearchDeleteSchedulesBoundedFanout requires the delete of a node with
// more than 100 relationships to record one access work item for the deleted
// node. The first slice of that work must schedule exactly 100 counterparts
// and keep the deleted node's work pending. Later slices must finish the
// fanout and leave no access or cleanup work.
func TestSearchDeleteSchedulesBoundedFanout(t *testing.T) {
	stores := newSearchStore(t)
	adapter, _, _, _ := newSearchIndex(t, stores)
	source := clock.Wall{}
	deleted := putSearchText(t, stores, "deleted hub", readerExcludedValue)
	counterparts := make(map[uuid.UUID]struct{}, deleteFanoutCounterparts)
	for range deleteFanoutCounterparts {
		counterpart := deleted
		counterpart.NodeID = uuid.Must(uuid.NewV7())
		writeSearchNode(t, stores, counterpart, "counterpart", readerExcludedValue)
		linkSearchNodes(t, stores, deleted.OrgID, deleted.NodeID, counterpart.NodeID)
		counterparts[counterpart.NodeID] = struct{}{}
	}
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(deleteFanoutPageBytes))
	runSearchWorkerWithin(t, worker, deleteFanoutSlices)

	if err := stores.Nodes.Delete(t.Context(), deleted.OrgID, deleted.NodeID); err != nil {
		t.Fatalf("delete a node with %d relationships: %v", deleteFanoutCounterparts, err)
	}
	store := stores.SearchWork(source)
	fanout := claimAll(t, store, searchdomain.WorkClassAccess)
	work, exists := fanout[deleted.NodeID]
	if len(fanout) != 1 || !exists || !work.Deleted || work.Phase != searchdomain.PhaseDependents {
		t.Fatalf("access work after the delete = %d items, deleted-node work %+v, want one deleted dependents item", len(fanout), work)
	}
	if err := worker.Process(t.Context(), work); err != nil {
		t.Fatalf("process the first fanout slice: %v", err)
	}
	scheduled := claimAllFor(t, store, searchdomain.WorkClassAccess, shortLease)
	scheduledCounterparts := 0
	for nodeID := range scheduled {
		if _, isCounterpart := counterparts[nodeID]; isCounterpart {
			scheduledCounterparts++
		}
	}
	if _, pending := scheduled[deleted.NodeID]; !pending || scheduledCounterparts != 100 {
		t.Fatalf("first fanout slice scheduled %d counterparts with deleted work pending %t, want 100 and pending", scheduledCounterparts, pending)
	}

	waitPastLeases(t, slices.Collect(maps.Values(scheduled))...)
	runSearchWorkerWithin(t, worker, deleteFanoutSlices)
	for _, class := range []searchdomain.WorkClass{searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup} {
		if remaining := claimAll(t, store, class); len(remaining) != 0 {
			t.Fatalf("%s work remains after the fanout: %d items", class, len(remaining))
		}
	}
}
