package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	// abandonedWriteDelay sets the wait after the worker abandons a delayed
	// write before the proxy sends it to the engine. It exceeds the
	// ten-second operation timeout and is shorter than migrationWriteHorizon.
	abandonedWriteDelay = 15 * time.Second
	// horizonClaimMargin adds time for the test to run slices after
	// migrationWriteHorizon ends and claim the delayed work again.
	horizonClaimMargin = 5 * time.Second
	// abandonedWriteSlices limits the number of slices the worker runs
	// before it abandons the delayed write.
	abandonedWriteSlices = 20
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

// TestSearchWorkerWaitsForAbandonedPageWrite delays page 1 of a node
// until the worker stops waiting at OperationTimeout. The proxy sends
// the abandoned write to the engine 15 seconds later. The test requires
// the worker to finish indexing the node without sending page 1 again.
func TestSearchWorkerWaitsForAbandonedPageWrite(t *testing.T) {
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, 0)
	proxy.forwardAbandonedAfter(abandonedWriteDelay)
	nodeID := uuid.Must(uuid.NewV7())
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	proxy.target(nodeID, 1)
	for range abandonedWriteSlices {
		_, err := env.Worker.RunSlice(t.Context())
		if _, until, _ := proxy.held(); !until.IsZero() {
			break
		}
		if err != nil {
			t.Fatalf("run a slice before the delayed write: %v", err)
		}
	}
	_, until, abandoned := proxy.held()
	if until.IsZero() || !abandoned {
		t.Fatalf("delayed write ended at %s abandoned %t, want the worker to abandon it", until, abandoned)
	}

	for deadline := time.Now().Add(migrationWriteHorizon + horizonClaimMargin); time.Now().Before(deadline); {
		claimed, err := env.Worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("run a slice after the abandoned write: %v", err)
		}
		if !claimed {
			time.Sleep(250 * time.Millisecond)
		}
	}
	env.runIdle(t)
	if forwardedAt, status := proxy.forwarded(); forwardedAt.IsZero() || status != http.StatusOK {
		t.Fatalf("the engine answered the abandoned write at %s with status %d, want status %d", forwardedAt, status, http.StatusOK)
	}
	pageWrites := 0
	for _, write := range proxy.writesOf(nodeID) {
		if write.ordinal == 1 {
			pageWrites++
		}
	}
	if pageWrites != 1 {
		t.Fatalf("the proxy received %d writes of page 1, want only the abandoned write", pageWrites)
	}
	requireIndexedNode(t, env, nodeID)
}
