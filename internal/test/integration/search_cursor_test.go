package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// cursorSlicesPerNode bounds the worker slices of one corpus node.
	// Creation and a metadata rescan can each schedule content work for the
	// node. Each content work item takes one slice and can retry once after
	// a generation change. The entry point's dependents phase and a metadata
	// rescan can each schedule access work for the node. Each access work
	// item takes a pages slice and a dependents slice and can retry once.
	cursorSlicesPerNode = 10
	// cursorFixtureSlices bounds the work of the bootstrap organizations and
	// workspaces, the entry point's own access and dependents slices, and
	// the metadata rescans.
	cursorFixtureSlices = 100
)

// putCursorCorpus stores count matching nodes under the caller's entry point
// and indexes them. The drain fails the test when work is still claimable
// after the slice limit.
func putCursorCorpus(t *testing.T, fixture queryFixture, query string, count int) []uuid.UUID {
	t.Helper()
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	entryID := entryPoint(t, fixture, workspace)
	nodes := make([]uuid.UUID, 0, count)
	for number := range count {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Cursor node %d", number), query, "excluded"))
	}
	drainSearchWork(t, fixture.Worker, cursorSlicesPerNode*count+cursorFixtureSlices)
	return nodes
}

// cursorSession decodes the cursor with the configured key and loads its
// durable session.
func cursorSession(t *testing.T, fixture queryFixture, cursor string) searchdomain.Session {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OPENSEARCH_CURSOR_KEY"))
	if err != nil {
		t.Fatalf("decode cursor key: %v", err)
	}
	sessionID, _, err := tools.NewSearchCursorCodec(key).Decode(cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	session, err := fixture.Stores.SearchSessions(clock.Wall{}, 15*time.Minute).Load(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return session
}

// TestSearchCursorExactSortValues requires every match of the first raw batch
// to contain three sort values under one point in time: a finite score that
// does not increase across the batch, the match's node ID, and a nonnegative
// integer shard document position. The next batch must repeat no sort
// position.
func TestSearchCursorExactSortValues(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	putCursorCorpus(t, fixture, "amber lattice", 150)
	session := cursorSession(t, fixture, callSearch(t, fixture.Harness, "amber lattice", "").Cursor)
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	batch, err := ranker.Read(t.Context(), session.Query, session.Snapshot, nil)
	if err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if len(batch.Hits) != 100 {
		t.Fatalf("batch returned %d matches, want 100", len(batch.Hits))
	}
	previousScore := math.Inf(1)
	for _, hit := range batch.Hits {
		previousScore = requireSortValues(t, hit, previousScore)
	}
	next, err := ranker.Read(t.Context(), session.Query, searchdomain.Snapshot{PITID: batch.PITID, Index: session.Snapshot.Index, QueryTokens: session.Snapshot.QueryTokens}, batch.Hits[99].Sort)
	if err != nil {
		t.Fatalf("read next batch: %v", err)
	}
	for _, hit := range next.Hits {
		if slices.ContainsFunc(batch.Hits, func(previous searchdomain.RankHit) bool { return bytes.Equal(previous.Sort, hit.Sort) }) {
			t.Fatalf("search_after repeated sort position %s", hit.Sort)
		}
	}
}

// requireSortValues checks the score, node ID, and shard document sort values
// of one match and returns its score. The score must not exceed
// previousScore.
func requireSortValues(t *testing.T, hit searchdomain.RankHit, previousScore float64) float64 {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(hit.Sort, &values); err != nil || len(values) != 3 {
		t.Fatalf("sort values %s are not score, node ID, and shard document", hit.Sort)
	}
	var score float64
	if err := json.Unmarshal(values[0], &score); err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
		t.Fatalf("sort score %s of node %s is not a finite number: %v", values[0], hit.NodeID, err)
	}
	if score > previousScore {
		t.Fatalf("sort score %v of node %s exceeds the previous score %v", score, hit.NodeID, previousScore)
	}
	if string(values[1]) != `"`+hit.NodeID.String()+`"` {
		t.Fatalf("sort node ID %s differs from match node %s", values[1], hit.NodeID)
	}
	var shardDocument int64
	if err := json.Unmarshal(values[2], &shardDocument); err != nil || shardDocument < 0 {
		t.Fatalf("sort shard document %s of node %s is not a nonnegative integer: %v", values[2], hit.NodeID, err)
	}
	return score
}

// TestSearchCursorOnePredictionPerSession requires one continuation to
// preserve the stored token-weight map and to advance the session version
// once.
func TestSearchCursorOnePredictionPerSession(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	putCursorCorpus(t, fixture, "velvet harbor", 80)
	first := callSearch(t, fixture.Harness, "velvet harbor", "")
	opened := cursorSession(t, fixture, first.Cursor)
	second := callSearch(t, fixture.Harness, "velvet harbor", first.Cursor)
	continued := cursorSession(t, fixture, second.Cursor)
	if !bytes.Equal(opened.Snapshot.QueryTokens, continued.Snapshot.QueryTokens) || continued.Version != opened.Version+1 {
		t.Fatalf("continuation changed the query tokens or skipped a version: %d to %d", opened.Version, continued.Version)
	}
}

// TestSearchCursorReplayAndConcurrency retries a lost response and runs
// concurrent calls for one cursor. Each committed page must replay exactly.
func TestSearchCursorReplayAndConcurrency(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	putCursorCorpus(t, fixture, "granite orchard", 120)
	first := callSearch(t, fixture.Harness, "granite orchard", "")
	second := callSearch(t, fixture.Harness, "granite orchard", first.Cursor)
	retried := callSearch(t, fixture.Harness, "granite orchard", first.Cursor)
	if !slices.Equal(second.IDs, retried.IDs) || second.Cursor != retried.Cursor {
		t.Fatalf("replay returned %v and %q, want %v and %q", retried.IDs, retried.Cursor, second.IDs, second.Cursor)
	}
	const callers = 4
	pages := make([]searchPage, callers)
	failures := make([]error, callers)
	var group sync.WaitGroup
	for caller := range callers {
		group.Go(func() { pages[caller], failures[caller] = trySearch(fixture.Harness, "granite orchard", second.Cursor) })
	}
	group.Wait()
	for caller := range callers {
		if failures[caller] != nil {
			t.Fatalf("concurrent caller %d failed: %v", caller, failures[caller])
		}
		if !slices.Equal(pages[caller].IDs, pages[0].IDs) || pages[caller].Cursor != pages[0].Cursor {
			t.Fatalf("concurrent caller %d received a different page", caller)
		}
	}
}
