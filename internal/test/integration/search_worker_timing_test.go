package integration

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// startDeadlineDelay delays one page write past the two-second slice
	// start deadline and below the ten-second operation timeout.
	startDeadlineDelay = 2500 * time.Millisecond
	// operationStopEarliest and operationStopLatest bound how long the
	// delayed write stays open before the worker abandons it. The worker
	// starts its ten-second timer just before the proxy receives the write,
	// and the proxy notices the cancel a little after it happens. Fifteen
	// seconds still separates the ten-second stop from the two-minute client
	// timeout.
	operationStopEarliest = 9500 * time.Millisecond
	operationStopLatest   = 15 * time.Second
	// resultWait bounds the wait for the first worker loop to return.
	resultWait = 2 * time.Minute
	// timingText projects to several pages at timingPageBytes.
	timingText = "worker timing text "
)

// delayedSlice is the end of the first worker loop of the operation test.
type delayedSlice struct {
	returned time.Time
	err      error
	ended    bool
}

// TestSearchWorkerSliceStopsAtStartDeadline delays the first page write of a
// multi-page node past the two-second slice budget. The slice must send no
// other page write, and the next claim must resume at ordinal 1.
func TestSearchWorkerSliceStopsAtStartDeadline(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, proxy, index := newDelayedSearchIndex(t, stores, startDeadlineDelay)
	fixture := putSearchText(t, stores, strings.Repeat(timingText, 40), readerExcludedValue)
	if pages := readSearchPages(t, stores, fixture.NodeID, timingPageBytes); len(pages) < 3 {
		t.Fatalf("node has %d pages, want at least 3", len(pages))
	}
	proxy.target(fixture.NodeID, 0)
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(timingPageBytes))
	if claimed, err := worker.RunSlice(t.Context()); err != nil || !claimed {
		t.Fatalf("run the first slice: claimed %t err %v", claimed, err)
	}
	if writes := proxy.writesOf(fixture.NodeID); len(writes) != 1 || writes[0].ordinal != 0 {
		t.Fatalf("the delayed slice sent %d page writes %+v, want only page 0", len(writes), writes)
	}
	store := stores.SearchWork(clock.Wall{})
	next := claimClassOf(t, store, searchdomain.WorkClassLive, fixture.NodeID)
	if next.Ordinal != 1 || next.Cursor == "" {
		t.Fatalf("next claim resumes at ordinal %d cursor %q, want ordinal 1 with a cursor", next.Ordinal, next.Cursor)
	}
	if err := store.Yield(t.Context(), next); err != nil {
		t.Fatalf("yield the inspected claim: %v", err)
	}
	runSearchWorkerUntilIdle(t, worker)
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), readSearchPages(t, stores, fixture.NodeID, timingPageBytes), 0)
}

// TestSearchWorkerStopsDelayedRequestAtOperationTimeout delays page 1 of node
// X until the client abandons the write. The worker must abandon it at ten
// seconds, start no other write of X before the slice returns, and resume
// the next claim of X at ordinal 1. A second worker must write a page of
// node Y while the write is open, and Y must be fully indexed afterward.
func TestSearchWorkerStopsDelayedRequestAtOperationTimeout(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, proxy, index := newDelayedSearchIndex(t, stores, 0)
	delayed := putSearchText(t, stores, strings.Repeat(timingText, 40), readerExcludedValue)
	proxy.target(delayed.NodeID, 1)
	settings := searchWorkerSettings(timingPageBytes)
	first := newSearchWorker(t, stores, adapter, clock.Wall{}, settings)
	results := make(chan delayedSlice, 1)
	go func() {
		for range 20 {
			_, err := first.RunSlice(t.Context())
			if _, until, _ := proxy.held(); !until.IsZero() || err != nil {
				results <- delayedSlice{returned: time.Now(), err: err, ended: !until.IsZero()}
				return
			}
		}
		results <- delayedSlice{returned: time.Now(), err: nil, ended: false}
	}()
	select {
	case <-proxy.started:
	case <-time.After(2 * time.Minute):
		t.Fatal("page 1 of the delayed node was not written within two minutes")
	}

	other := putSearchText(t, stores, shortTimingText, readerExcludedValue)
	second := newSearchWorker(t, stores, adapter, clock.Wall{}, settings)
	for len(proxy.writesOf(other.NodeID)) == 0 {
		if _, until, _ := proxy.held(); !until.IsZero() {
			break
		}
		if _, err := second.RunSlice(t.Context()); err != nil {
			t.Fatalf("run a slice of the second worker: %v", err)
		}
	}

	var result delayedSlice
	select {
	case result = <-results:
	case <-time.After(resultWait):
		t.Fatalf("the first worker loop did not return within %s", resultWait)
	}
	from, until, abandoned := proxy.held()
	otherWritten := false
	for _, write := range proxy.writesOf(other.NodeID) {
		if write.received.After(from) && write.received.Before(until) {
			otherWritten = true
		}
	}
	if !otherWritten {
		t.Fatalf("the second worker wrote no page of another node while the delayed write was open from %s to %s", from, until)
	}
	if !result.ended || !abandoned {
		t.Fatalf("delayed write ended %t abandoned %t, slice error %v; want the client to abandon it", result.ended, abandoned, result.err)
	}
	if open := until.Sub(from); open < operationStopEarliest || open > operationStopLatest {
		t.Fatalf("the worker abandoned the delayed write after %s, want about ten seconds", open)
	}
	for _, write := range proxy.writesOf(delayed.NodeID) {
		if write.received.After(from) && write.received.Before(result.returned) {
			t.Fatalf("the worker sent page %d at %s while the delayed write was open", write.ordinal, write.received)
		}
	}
	store := stores.SearchWork(clock.Wall{})
	next := claimClassOf(t, store, searchdomain.WorkClassLive, delayed.NodeID)
	if next.Ordinal != 1 || next.Cursor == "" {
		t.Fatalf("next claim resumes at ordinal %d cursor %q, want ordinal 1 with a cursor", next.Ordinal, next.Cursor)
	}
	if err := store.Yield(t.Context(), next); err != nil {
		t.Fatalf("yield the inspected claim: %v", err)
	}
	runSearchWorkerUntilIdle(t, first)
	requireIndexedPages(t, searchNodePages(t, client, index, delayed.NodeID, false), readSearchPages(t, stores, delayed.NodeID, timingPageBytes), 0)
	requireIndexedPages(t, searchNodePages(t, client, index, other.NodeID, false), readSearchPages(t, stores, other.NodeID, timingPageBytes), 0)
}
