package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// rankingWaitDeadline limits how long a wait can take to confirm indexing
	// or one replacement state.
	rankingWaitDeadline = 15 * time.Minute
	// rankingPoll is the interval between two checks during a wait.
	rankingPoll = time.Second
)

func (r *rankingRun) servingIndex(t *testing.T) string {
	t.Helper()
	index, err := r.stores.ServingSearchIndex(t.Context())
	if err != nil {
		t.Fatalf("read serving search index: %v", err)
	}
	return index
}

// waitIndexed waits until the serving index has an active page for every
// node in nodes.
func (r *rankingRun) waitIndexed(t *testing.T, nodes []uuid.UUID) {
	t.Helper()
	index := r.servingIndex(t)
	identifiers := make([]string, 0, len(nodes))
	for _, nodeID := range nodes {
		identifiers = append(identifiers, nodeID.String())
	}
	filters := []json.RawMessage{
		encodeTrace(t, traceTerms{Terms: map[string][]string{"node_id": identifiers}}),
		json.RawMessage(`{"term":{"retired":false}}`),
	}
	body := traceSearch{Size: traceHits, Explain: false, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: filters, Should: []json.RawMessage{}, MinimumShouldMatch: 0,
	}}}
	started := clock.Now()
	for clock.Since(started) < rankingWaitDeadline {
		if _, err := r.client.Indices.Refresh(t.Context(), &opensearchapi.IndicesRefreshReq{Index: []string{index}}); err != nil {
			t.Fatalf("refresh %s: %v", index, err)
		}
		indexed := len(rankedTraceNodes(t, r.traceSearch(t, index, body)))
		if indexed == len(nodes) {
			t.Logf("regression indexed nodes=%d wall_ms=%d", indexed, clock.Since(started).Milliseconds())
			return
		}
		time.Sleep(rankingPoll)
	}
	t.Fatalf("the serving index did not have every regression node within %s", rankingWaitDeadline)
}

// beginRebuild begins one full replacement through the production store and
// deletes its target index when the test ends.
func (r *rankingRun) beginRebuild(t *testing.T) searchdomain.Rebuild {
	t.Helper()
	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Reason: "relevance regression"}
	rebuild, err := r.stores.SearchRebuilds(clock.Wall{}).BeginRebuild(t.Context(), request)
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() {
		deletion := opensearchapi.IndicesDeleteReq{Indices: []string{rebuild.TargetIndex}}
		_, _ = r.client.Indices.Delete(context.WithoutCancel(t.Context()), deletion)
	})
	return rebuild
}

// waitRebuild waits until done accepts the replacement state while the
// production search workers run. It passes found as false when the replacement
// does not exist.
func (r *rankingRun) waitRebuild(t *testing.T, done func(searchdomain.Rebuild, bool) bool) {
	t.Helper()
	rebuilds := r.stores.SearchRebuilds(clock.Wall{})
	started := clock.Now()
	for clock.Since(started) < rankingWaitDeadline {
		current, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if done(current, found) {
			t.Logf("regression replacement found=%t state=%s wall_ms=%d", found, current.State, clock.Since(started).Milliseconds())
			return
		}
		time.Sleep(rankingPoll)
	}
	t.Fatalf("the index replacement did not enter the expected state within %s", rankingWaitDeadline)
}
