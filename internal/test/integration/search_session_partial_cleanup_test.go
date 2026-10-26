package integration_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// partialCleanupQuery is the included text of every node in the corpus.
	partialCleanupQuery = "cobalt ferry partial cleanup"
	// partialCleanupNodes exceeds the 25 results of one page. The first page
	// returns a cursor.
	partialCleanupNodes = 30
	// partialCleanupIdle and partialCleanupAbsolute are the short session
	// deadlines of the test process.
	partialCleanupIdle     = 3 * time.Second
	partialCleanupAbsolute = 6 * time.Second
	// partialCleanupMargin follows the end of the expiry minute before the
	// sweep searches start.
	partialCleanupMargin = time.Second
	// partialCleanupSweeps bounds the new searches that may run the sweep.
	partialCleanupSweeps = 4
	// expiredCursorMessage is part of the tack_search error for a closed
	// session.
	expiredCursorMessage = "the search cursor expired"
)

// TestSearchSweepRemovesPartlyCleanedSession stops the cleanup of an open
// session after one slice through the production session store. The slice
// deletes the replay record and the query token chunks, which sort before
// the visited markers, and keeps the header. The cursor of that session must
// return the expired cursor error, and a later search must sweep the session
// after its idle deadline.
func TestSearchSweepRemovesPartlyCleanedSession(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", partialCleanupIdle.String())
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", partialCleanupAbsolute.String())
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range partialCleanupNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Partial cleanup node %d", number), partialCleanupQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*partialCleanupNodes+100)

	first := integration.CallSearch(t, fixture.Harness, partialCleanupQuery, "")
	session := integration.CursorSession(t, fixture, first.Cursor)
	stopCleanupAfterOneSlice(t, fixture, session, len(first.IDs))

	if _, err := integration.TrySearch(fixture.Harness, partialCleanupQuery, first.Cursor); err == nil || !strings.Contains(err.Error(), expiredCursorMessage) {
		t.Errorf("the cursor of the partly deleted session returned error %v, want %q", err, expiredCursorMessage)
	}
	waitForTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+partialCleanupMargin))
	store := fixture.Stores.SearchSessions(clock.Wall{}, partialCleanupIdle)
	for attempt := range partialCleanupSweeps {
		integration.CallSearch(t, fixture.Harness, fmt.Sprintf("%s sweep %d", partialCleanupQuery, attempt), "")
		_, err := store.Load(integration.WithTestOperation(t, t.Context()), session.ID)
		if errors.Is(err, searchdomain.ErrSessionNotFound) {
			return
		}
		t.Logf("sweep search %d left session %s: %v", attempt+1, session.ID, err)
	}
	t.Fatalf("%d sweep searches did not delete the partly cleaned session %s", partialCleanupSweeps, session.ID)
}

// stopCleanupAfterOneSlice closes the point in time of session, marks the
// session closing, and runs one cleanup slice of limit child records, as the
// sweep does before it stops between slices. The first page stores one
// visited marker for each of its limit results. The slice reads limit
// records and keeps the header.
func stopCleanupAfterOneSlice(t *testing.T, fixture integration.QueryFixture, session searchdomain.Session, limit int) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Hour, TokenBytes: 64 << 10, BatchSize: 100})
	if err := ranker.Close(ctx, session.Snapshot); err != nil {
		t.Fatalf("close the point in time of session %s: %v", session.ID, err)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, partialCleanupIdle)
	if err := store.BeginCleanup(ctx, session.ID); err != nil {
		t.Fatalf("mark session %s closing: %v", session.ID, err)
	}
	done, err := store.CleanupSlice(ctx, session.ID, limit)
	if err != nil || done {
		t.Fatalf("one cleanup slice of session %s returned done %t and error %v, want a partly deleted session", session.ID, done, err)
	}
}

// waitForTime waits until instant and fails the test if its context ends
// first.
func waitForTime(t *testing.T, instant time.Time) {
	t.Helper()
	select {
	case <-t.Context().Done():
		t.Fatalf("the test ended before %s", instant.UTC().Format(time.RFC3339Nano))
	case <-time.After(instant.Sub(clock.Now())):
	}
}
