package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// abandonedWriteSlices limits how many slices abandonPageOne runs while
	// waiting for the worker to abandon the delayed write.
	abandonedWriteSlices = 20
	// idleClaimPause is the pause after a slice that fails or does not claim
	// work.
	idleClaimPause = 250 * time.Millisecond
)

// abandonedPage records a node and its test environment after the worker
// abandons the first write of page 1 at OperationTimeout.
type abandonedPage struct {
	env    *migrationEnv
	proxy  *pageDelayProxy
	nodeID uuid.UUID
	// abandonedAt is when the proxy observes the worker stop waiting.
	abandonedAt time.Time
	// generation is the search generation of the abandoned write.
	generation int64
}

// abandonPageOne creates a worker with horizon as WriteCompletionHorizon,
// stores a node with several pages, and runs slices until the worker
// abandons page 1. When forwardAfter is positive, the proxy sends that write
// to the engine after that delay. When forwardAfter is zero, the proxy does
// not send it.
func abandonPageOne(t *testing.T, horizon, forwardAfter time.Duration) abandonedPage {
	t.Helper()
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, 0)
	env.WriteHorizon = horizon
	env.Worker = env.newWorker(t, env.Stores)
	proxy.forwardAbandonedAfter(forwardAfter)
	// Organization rescans from the two property definition writes must
	// finish while the node does not exist. A rescan after node creation
	// starts a new content revision.
	env.runIdle(t)
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
	writes := pageWrites(proxy, nodeID, 1)
	return abandonedPage{env: env, proxy: proxy, nodeID: nodeID, abandonedAt: until, generation: writes[0].generation}
}

// runSlicesUntil runs worker slices until deadline. It logs slice errors and
// checks done after a slice that fails or does not claim work. It returns
// true when a non-nil done reports completion, or false at the deadline.
func runSlicesUntil(t *testing.T, env *migrationEnv, deadline time.Time, done func() bool) bool {
	t.Helper()
	for time.Now().Before(deadline) {
		claimed, err := env.Worker.RunSlice(t.Context())
		if err != nil {
			t.Logf("run a slice: %v", err)
		}
		if claimed && err == nil {
			continue
		}
		if done != nil && done() {
			return true
		}
		time.Sleep(idleClaimPause)
	}
	return false
}

// pageWrites returns the writes the proxy received for nodeID and ordinal.
func pageWrites(proxy *pageDelayProxy, nodeID uuid.UUID, ordinal uint64) []delayedPageWrite {
	writes := make([]delayedPageWrite, 0, 1)
	for _, write := range proxy.writesOf(nodeID) {
		if write.ordinal == ordinal {
			writes = append(writes, write)
		}
	}
	return writes
}

// storedGenerations refreshes the index and returns the search generation of
// each active page of nodeID.
func storedGenerations(t *testing.T, env *migrationEnv, nodeID uuid.UUID) []int64 {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	if _, err := env.Client.Indices.Refresh(ctx, &opensearchapi.IndicesRefreshReq{Indices: []string{env.Index}}); err != nil {
		t.Fatalf("refresh %s: %v", env.Index, err)
	}
	query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}}}`, nodeID.String())
	response, err := env.Client.Search(ctx, &opensearchapi.SearchReq{Indices: []string{env.Index}, BodyReader: strings.NewReader(query)})
	if err != nil {
		t.Fatalf("search pages of node %s: %v", nodeID, err)
	}
	generations := make([]int64, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var page struct {
			SearchGeneration int64 `json:"search_generation"`
		}
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			t.Fatalf("decode page of node %s: %v", nodeID, err)
		}
		generations = append(generations, page.SearchGeneration)
	}
	return generations
}
