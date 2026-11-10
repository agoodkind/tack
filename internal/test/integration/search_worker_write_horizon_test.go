package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	// abandonedWriteDelay is the proxy's wait after the worker abandons a write
	// until the proxy sends it to the engine. It exceeds the ten-second
	// OperationTimeout.
	abandonedWriteDelay = 15 * time.Second
	// storedWriteHorizon sets WriteCompletionHorizon for tests in which the
	// proxy sends the abandoned write. It exceeds abandonedWriteDelay plus
	// storedWriteMargin.
	storedWriteHorizon = 60 * time.Second
	// storedWriteMargin bounds the allowed indexing time after the proxy sends
	// the abandoned write to the engine.
	storedWriteMargin = 15 * time.Second
)

// forwardAbandonedAfter sets the delay before the proxy sends an
// abandoned delayed write to the engine.
func (proxy *pageDelayProxy) forwardAbandonedAfter(delay time.Duration) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.forwardAfter = delay
}

// forwardAbandoned sends an abandoned delayed write to the engine after
// forwardAfter when forwardAfter is positive. Client cancellation does
// not end its request context. It records when the engine responds and
// the HTTP status of the response.
func (proxy *pageDelayProxy) forwardAbandoned(request *http.Request) {
	proxy.mutex.Lock()
	delay := proxy.forwardAfter
	proxy.mutex.Unlock()
	if delay <= 0 {
		return
	}
	time.Sleep(delay)
	recorder := httptest.NewRecorder()
	proxy.engine.ServeHTTP(recorder, request.WithContext(context.WithoutCancel(request.Context())))
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.forwardedAt, proxy.forwardStatus = time.Now(), recorder.Code
}

// forwarded returns the time the engine answered the abandoned delayed
// write and the HTTP status of that answer. A zero time means the proxy
// has not recorded an engine response.
func (proxy *pageDelayProxy) forwarded() (time.Time, int) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return proxy.forwardedAt, proxy.forwardStatus
}

// TestSearchWorkerWaitsForAbandonedPageWrite requires exactly one write of
// page 1 and every page indexed within 30 seconds of abandonment. The proxy
// sends the abandoned write to the engine after 15 seconds under a 60-second
// horizon. On main, the next claim waits the full horizon and misses the
// 30-second bound.
func TestSearchWorkerWaitsForAbandonedPageWrite(t *testing.T) {
	abandoned := abandonPageOne(t, storedWriteHorizon, abandonedWriteDelay)
	env, proxy, nodeID := abandoned.env, abandoned.proxy, abandoned.nodeID
	bound := abandoned.abandonedAt.Add(abandonedWriteDelay + storedWriteMargin)
	indexed := runSlicesUntil(t, env, bound, func() bool { return indexMismatch(t, env, nodeID) == "" })
	forwardedAt, status := proxy.forwarded()
	if forwardedAt.IsZero() || status != http.StatusOK {
		t.Fatalf("the engine answered the abandoned write at %s with status %d, want status %d", forwardedAt, status, http.StatusOK)
	}
	if !indexed {
		t.Fatalf("node %s was not indexed by %s, %s after the abandon at %s: %s",
			nodeID, bound, bound.Sub(abandoned.abandonedAt), abandoned.abandonedAt, indexMismatch(t, env, nodeID))
	}
	if writes := pageWrites(proxy, nodeID, 1); len(writes) != 1 {
		t.Fatalf("the proxy received %d writes of page 1 %+v, want only the abandoned write", len(writes), writes)
	}
	env.runIdle(t)
	requireIndexedNode(t, env, nodeID)
}

// writesOf returns the page writes of nodeID the proxy received.
func (proxy *pageDelayProxy) writesOf(nodeID uuid.UUID) []delayedPageWrite {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	writes := make([]delayedPageWrite, 0, len(proxy.writes))
	for _, write := range proxy.writes {
		if write.nodeID == nodeID.String() {
			writes = append(writes, write)
		}
	}
	return writes
}
