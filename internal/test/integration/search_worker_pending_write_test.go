package integration_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// droppedWriteHorizon sets WriteCompletionHorizon for the test in which the
	// proxy never sends the abandoned write to the engine.
	droppedWriteHorizon = 20 * time.Second
	// abandonClockSkew allows for the difference between the proxy's observed
	// abandon time and the worker's recorded abandon time.
	abandonClockSkew = time.Second
	// resendMargin bounds the allowed time after the horizon ends for the
	// worker to resend page 1 and finish indexing the node.
	resendMargin = 15 * time.Second
)

// TestSearchWorkerResendsDroppedPageWriteAfterHorizon drops the abandoned
// write of page 1. It rejects another page 1 write during the 20-second
// horizon, allowing one second for abandonClockSkew. It then requires exactly
// one resend and every page indexed within 15 seconds after the horizon.
func TestSearchWorkerResendsDroppedPageWriteAfterHorizon(t *testing.T) {
	abandoned := abandonPageOne(t, droppedWriteHorizon, 0)
	env, proxy, nodeID := abandoned.env, abandoned.proxy, abandoned.nodeID
	horizonEnd := abandoned.abandonedAt.Add(droppedWriteHorizon)
	runSlicesUntil(t, env, horizonEnd.Add(-abandonClockSkew), nil)
	if writes := pageWrites(proxy, nodeID, 1); len(writes) != 1 {
		t.Fatalf("the proxy received %d writes of page 1 %+v before the horizon ended at %s, want only the abandoned write",
			len(writes), writes, horizonEnd)
	}
	bound := horizonEnd.Add(resendMargin)
	if !runSlicesUntil(t, env, bound, func() bool { return indexMismatch(t, env, nodeID) == "" }) {
		t.Fatalf("node %s was not indexed by %s: %s", nodeID, bound, indexMismatch(t, env, nodeID))
	}
	writes := pageWrites(proxy, nodeID, 1)
	if len(writes) != 2 || writes[1].received.Before(horizonEnd.Add(-abandonClockSkew)) {
		t.Fatalf("the proxy received writes of page 1 %+v, want the abandoned write and one write after %s", writes, horizonEnd)
	}
	env.runIdle(t)
	requireIndexedNode(t, env, nodeID)
}

// TestSearchWorkerKeepsPendingPageAcrossGenerations checks that an access
// change preserves the pending write of page 1. Stores.Relationships.Add
// adds a relationship that raises the node's generation. The proxy sends
// the abandoned write after 15 seconds under a 60-second horizon. The test
// requires later page 1 writes to follow the engine's response and the last
// write to use a higher generation. It also requires full indexing with a
// page above the abandoned generation within 30 seconds of abandonment.
// On main, the newer claim creates page 1 while the abandoned request still
// waits in the proxy.
func TestSearchWorkerKeepsPendingPageAcrossGenerations(t *testing.T) {
	abandoned := abandonPageOne(t, storedWriteHorizon, abandonedWriteDelay)
	env, proxy, nodeID := abandoned.env, abandoned.proxy, abandoned.nodeID
	otherID := uuid.Must(uuid.NewV7())
	env.putNode(t, otherID, shortTimingText)
	relationship := &node.Relationship{
		OrgID: env.OrgID, SourceID: nodeID, RelationType: uniqueKey("r"), TargetID: otherID,
		CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil,
	}
	if err := env.Stores.Relationships.Add(integration.WithTestOperation(t, t.Context()), relationship); err != nil {
		t.Fatalf("add a relationship of node %s: %v", nodeID, err)
	}
	advanced := func() bool {
		if indexMismatch(t, env, nodeID) != "" {
			return false
		}
		return slices.Max(storedGenerations(t, env, nodeID)) > abandoned.generation
	}
	bound := abandoned.abandonedAt.Add(abandonedWriteDelay + storedWriteMargin)
	completed := runSlicesUntil(t, env, bound, advanced)
	forwardedAt, status := proxy.forwarded()
	if forwardedAt.IsZero() || status != http.StatusOK {
		t.Fatalf("the engine answered the abandoned write at %s with status %d, want status %d", forwardedAt, status, http.StatusOK)
	}
	writes := pageWrites(proxy, nodeID, 1)
	for _, write := range writes[1:] {
		if write.received.Before(forwardedAt) {
			t.Fatalf("the worker sent page 1 at generation %d at %s, before the engine answered the abandoned write at %s",
				write.generation, write.received, forwardedAt)
		}
	}
	if latest := writes[len(writes)-1]; latest.generation <= abandoned.generation {
		t.Fatalf("the last write of page 1 has generation %d, want a generation above %d", latest.generation, abandoned.generation)
	}
	if !completed {
		t.Fatalf("node %s did not finish a generation above %d by %s, page generations %v",
			nodeID, abandoned.generation, bound, storedGenerations(t, env, nodeID))
	}
	env.runIdle(t)
	requireIndexedNode(t, env, nodeID)
}
