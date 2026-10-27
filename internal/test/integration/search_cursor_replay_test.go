package integration_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

const (
	// replayQuery is the included text of every node in the replay corpus.
	replayQuery = "granite orchard"
	// replayNodes is the matching node count of the replay corpus.
	replayNodes = 120
	// replayCallers is the number of concurrent calls with one cursor.
	replayCallers = 4
	// replayCommits is the number of pages the session commits.
	replayCommits = 4
)

// TestSearchCursorReplayAndConcurrency requires a retry after a lost response
// to replay the committed page exactly. Concurrent calls with one cursor must
// all receive the same page. A replay, including a replay in a restarted
// process, must leave the stored session records unchanged. After four
// commits the session must store one header, its token chunks, one visited
// marker per returned node, one replay record, and one expiry key.
func TestSearchCursorReplayAndConcurrency(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range replayNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Replay node %d", number), replayQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*replayNodes+100)

	first := integration.CallSearch(t, fixture.Harness, replayQuery, "")
	second := integration.CallSearch(t, fixture.Harness, replayQuery, first.Cursor)
	sessionID := integration.CursorSession(t, fixture, second.Cursor).ID
	committed := sessionRecords(t, sessionID)
	retried := integration.CallSearch(t, fixture.Harness, replayQuery, first.Cursor)
	if !slices.Equal(second.IDs, retried.IDs) || second.Cursor != retried.Cursor {
		t.Fatalf("replay returned %v and %q, want %v and %q", retried.IDs, retried.Cursor, second.IDs, second.Cursor)
	}
	requireSessionUnchanged(t, "a retry after a lost response", committed, sessionRecords(t, sessionID))

	pages := make([]integration.SearchPage, replayCallers)
	failures := make([]error, replayCallers)
	var group sync.WaitGroup
	for caller := range replayCallers {
		group.Go(func() {
			pages[caller], failures[caller] = integration.TrySearch(fixture.Harness, replayQuery, second.Cursor)
		})
	}
	group.Wait()
	for caller := range replayCallers {
		if failures[caller] != nil {
			t.Fatalf("concurrent caller %d failed: %v", caller, failures[caller])
		}
		if !slices.Equal(pages[caller].IDs, pages[0].IDs) || pages[caller].Cursor != pages[0].Cursor {
			t.Fatalf("concurrent caller %d received a different page", caller)
		}
	}

	advanced := sessionRecords(t, sessionID)
	restarted := integration.ProcessHarness(t, fixture.Harness, fixture.Config)
	replayed := integration.CallSearch(t, restarted, replayQuery, second.Cursor)
	if !slices.Equal(replayed.IDs, pages[0].IDs) || replayed.Cursor != pages[0].Cursor {
		t.Fatalf("a restarted process replayed %v, want %v", replayed.IDs, pages[0].IDs)
	}
	requireSessionUnchanged(t, "a replay in a restarted process", advanced, sessionRecords(t, sessionID))
	fourth := integration.CallSearch(t, restarted, replayQuery, pages[0].Cursor)
	results := slices.Concat(first.IDs, second.IDs, pages[0].IDs, fourth.IDs)
	requireBoundedSession(t, sessionRecords(t, sessionID), results, replayCommits)
}
