package integration

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// timingPageBytes keeps each page small. A node with a few hundred bytes
	// of text has several pages.
	timingPageBytes = 128
	// cleanupBatchDocuments is the most old page documents that one cleanup
	// slice retires. The slice leaves any remaining documents for a later
	// slice.
	cleanupBatchDocuments = 100
	// claimWait bounds the wait for released work to become claimable again
	// after its retry delay.
	claimWait = 30 * time.Second
	// shortTimingText fits on one page at timingPageBytes.
	shortTimingText = "short timing text"
)

// claimClassOf claims work of class until it claims the work of nodeID, and
// yields every other claim. It retries until released work passes its
// retry delay.
func claimClassOf(t *testing.T, store *fdbadapter.SearchWorkStore, class searchdomain.WorkClass, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	for deadline := time.Now().Add(claimWait); time.Now().Before(deadline); {
		work, err := store.Claim(t.Context(), class, "timing-inspector", time.Minute)
		if errors.Is(err, searchdomain.ErrNoWork) {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("claim %s work of %s: %v", class, nodeID, err)
		}
		if work.NodeID == nodeID {
			return work
		}
		if err := store.Yield(t.Context(), work); err != nil {
			t.Fatalf("yield %s work of %s: %v", class, work.NodeID, err)
		}
	}
	t.Fatalf("%s work of %s was not claimable within %s", class, nodeID, claimWait)
	return searchdomain.Work{}
}

// TestSearchCleanupYieldsAfter100Documents indexes a node with more than 100
// pages, shortens it to one page, and runs its cleanup one slice at a time.
// The first slice must retire exactly 100 old documents and leave the
// cleanup work claimable. Later slices retire the rest.
func TestSearchCleanupYieldsAfter100Documents(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	fixture := putSearchText(t, stores, strings.Repeat("cleanup yield text ", 700), readerExcludedValue)
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(timingPageBytes))
	runSearchWorkerWithin(t, worker, 2000)
	original := searchNodePages(t, client, index, fixture.NodeID, false)
	if len(original) <= cleanupBatchDocuments {
		t.Fatalf("node indexed %d pages, want more than %d", len(original), cleanupBatchDocuments)
	}

	writeSearchNode(t, stores, fixture, shortTimingText, readerExcludedValue)
	work := stores.SearchWork(clock.Wall{})
	drainClass(t, work, worker, searchdomain.WorkClassLive)
	before := len(searchNodePages(t, client, index, fixture.NodeID, true))
	if err := worker.Process(t.Context(), claimClassOf(t, work, searchdomain.WorkClassCleanup, fixture.NodeID)); err != nil {
		t.Fatalf("process the first cleanup slice: %v", err)
	}
	if retired := len(searchNodePages(t, client, index, fixture.NodeID, true)) - before; retired != cleanupBatchDocuments {
		t.Fatalf("the first cleanup slice retired %d documents, want %d", retired, cleanupBatchDocuments)
	}
	if err := worker.Process(t.Context(), claimClassOf(t, work, searchdomain.WorkClassCleanup, fixture.NodeID)); err != nil {
		t.Fatalf("process the second cleanup slice: %v", err)
	}
	drainClass(t, work, worker, searchdomain.WorkClassCleanup)

	if retired := searchNodePages(t, client, index, fixture.NodeID, true); len(retired) != len(original) {
		t.Fatalf("cleanup retired %d documents, want the %d documents of the old revision", len(retired), len(original))
	}
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), readSearchPages(t, stores, fixture.NodeID, timingPageBytes), 0)
}
