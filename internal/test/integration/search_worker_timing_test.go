package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// startDeadlineDelay delays one page write past the two-second slice
	// start deadline and below the ten-second operation timeout.
	startDeadlineDelay = 2500 * time.Millisecond
	// operationStopEarliest and operationStopLatest bound how long the
	// delayed write stays open before the worker abandons it. The worker
	// starts its ten-second timer just before the proxy receives the write.
	// The proxy observes cancellation shortly after the client cancels.
	// The 15-second upper bound is below the two-minute client timeout.
	// A write that the client timeout stopped would stay open for two
	// minutes and fail the upper bound.
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
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, startDeadlineDelay)
	nodeID := uuid.Must(uuid.NewV7())
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	if pages := env.readerPages(t, nodeID); len(pages) < 3 {
		t.Fatalf("node has %d pages, want at least 3", len(pages))
	}
	proxy.target(nodeID, 0)
	if claimed, err := env.Worker.RunSlice(t.Context()); err != nil || !claimed {
		t.Fatalf("run the first slice: claimed %t err %v", claimed, err)
	}
	if writes := proxy.writesOf(nodeID); len(writes) != 1 || writes[0].ordinal != 0 {
		t.Fatalf("the delayed slice sent %d page writes %+v, want only page 0", len(writes), writes)
	}
	store := env.Stores.SearchWork(clock.Wall{})
	next := claimClassOf(t, store, searchdomain.WorkClassLive, nodeID)
	if next.Ordinal != 1 || next.Cursor == "" {
		t.Fatalf("next claim resumes at ordinal %d cursor %q, want ordinal 1 with a cursor", next.Ordinal, next.Cursor)
	}
	if err := store.Yield(t.Context(), next); err != nil {
		t.Fatalf("yield the inspected claim: %v", err)
	}
	env.runIdle(t)
	requireIndexedNode(t, env, nodeID)
}

// TestSearchWorkerStopsDelayedRequestAtOperationTimeout delays page 1 of node
// X until the client abandons the write. The worker must abandon it at ten
// seconds, start no other write of X before the slice returns, and resume
// the next claim of X at ordinal 1. A second worker must write a page of
// node Y while the write is open, and Y must be fully indexed afterward.
func TestSearchWorkerStopsDelayedRequestAtOperationTimeout(t *testing.T) {
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, 0)
	delayedID := uuid.Must(uuid.NewV7())
	env.putNode(t, delayedID, strings.Repeat(timingText, 40))
	proxy.target(delayedID, 1)
	first := env.Worker
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

	otherID := uuid.Must(uuid.NewV7())
	env.putNode(t, otherID, shortTimingText)
	second := env.newWorker(t, env.Stores)
	for len(proxy.writesOf(otherID)) == 0 {
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
	for _, write := range proxy.writesOf(otherID) {
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
	for _, write := range proxy.writesOf(delayedID) {
		if write.received.After(from) && write.received.Before(result.returned) {
			t.Fatalf("the worker sent page %d at %s while the delayed write was open", write.ordinal, write.received)
		}
	}
	store := env.Stores.SearchWork(clock.Wall{})
	next := claimClassOf(t, store, searchdomain.WorkClassLive, delayedID)
	if next.Ordinal != 1 || next.Cursor == "" {
		t.Fatalf("next claim resumes at ordinal %d cursor %q, want ordinal 1 with a cursor", next.Ordinal, next.Cursor)
	}
	if err := store.Yield(t.Context(), next); err != nil {
		t.Fatalf("yield the inspected claim: %v", err)
	}
	env.runIdle(t)
	requireIndexedNode(t, env, delayedID)
	requireIndexedNode(t, env, otherID)
}
