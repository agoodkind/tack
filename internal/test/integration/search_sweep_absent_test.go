package integration_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// absentSweepQuery is the included text of every node in the corpus.
	absentSweepQuery = "pewter sluice absent sweep"
	// absentSweepNodes exceeds the 25 results of one page. Each page returns
	// a cursor and leaves its session and point in time open.
	absentSweepNodes = 30
	// absentSweepSessions is the number of sessions that expire. The proxy
	// stops one sweep on the first session, and that sweep later loads the
	// second session.
	absentSweepSessions = 2
	// absentSweepIdle and absentSweepAbsolute are the short session
	// deadlines of the test process.
	absentSweepIdle     = 3 * time.Second
	absentSweepAbsolute = 6 * time.Second
	// absentSweepMargin follows the end of the expiry minute before the
	// test arms the proxy.
	absentSweepMargin = time.Second
	// absentSweepDeadline and absentSweepRequestTimeout outlast the open
	// delete request.
	absentSweepDeadline       = "1m"
	absentSweepRequestTimeout = "40s"
	// absentSweepHoldTimeout limits the wait for the open delete request.
	absentSweepHoldTimeout = 30 * time.Second
)

// searchOutcome is the page or the failure of one tack_search call.
type searchOutcome struct {
	page integration.SearchPage
	err  error
}

// TestSearchSweepSkipsSessionAnotherSweepDeleted opens two sessions through
// tack_search and waits past their expiry minute. The proxy keeps search X's
// delete request open while its sweep closes the first listed session.
// Search Y then lists the same sessions and deletes both. After the proxy
// releases the delete request, search X's sweep loads the second session.
// The second session no longer exists. The test requires both searches to
// return results and verifies that both sessions do not exist. It requires
// search X's sweep to log exactly one Debug skip record. It rejects every
// ERROR record with a message that starts with the "search." prefix.
func TestSearchSweepSkipsSessionAnotherSweepDeleted(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", absentSweepIdle.String())
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", absentSweepAbsolute.String())
	t.Setenv("OPENSEARCH_QUERY_DEADLINE", absentSweepDeadline)
	t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", absentSweepRequestTimeout)
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range absentSweepNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Absent sweep node %d", number), absentSweepQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*absentSweepNodes+100)
	harness, proxy := newPredictHoldHarness(t, fixture)

	expired := make([]searchdomain.Session, 0, absentSweepSessions)
	for range absentSweepSessions {
		page := integration.CallSearch(t, harness, absentSweepQuery, "")
		expired = append(expired, integration.CursorSession(t, fixture, page.Cursor))
	}
	latest := expired[len(expired)-1].IdleDeadline
	waitForTime(t, latest.Truncate(time.Minute).Add(time.Minute+absentSweepMargin))

	logged, restoreLog := captureSweepLog(t)
	first, second := interleaveSweeps(t, harness, proxy)
	restoreLog()

	for name, outcome := range map[string]searchOutcome{"X": first, "Y": second} {
		if outcome.err != nil || len(outcome.page.IDs) == 0 {
			t.Errorf("search %s returned %d results and error %v", name, len(outcome.page.IDs), outcome.err)
		}
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, absentSweepIdle)
	for _, session := range expired {
		if _, err := store.Load(integration.WithTestOperation(t, t.Context()), session.ID); !errors.Is(err, searchdomain.ErrSessionNotFound) {
			t.Errorf("expired session %s remains after both sweeps: %v", session.ID, err)
		}
	}
	requireSweepSkip(t, logged.contents())
}

// interleaveSweeps arms the proxy and starts search X. It waits until the
// proxy keeps the point in time delete request of the sweep of X open, runs
// search Y to completion, releases the request, and returns the outcome of
// both searches.
func interleaveSweeps(t *testing.T, harness *integration.MCPHarness, proxy *predictHoldProxy) (searchOutcome, searchOutcome) {
	t.Helper()
	proxy.armDelete()
	searched := make(chan searchOutcome, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				searched <- searchOutcome{page: integration.SearchPage{}, err: fmt.Errorf("search X panicked: %v", recovered)}
			}
		}()
		page, err := integration.TrySearch(harness, absentSweepQuery, "")
		searched <- searchOutcome{page: page, err: err}
	}()
	select {
	case <-proxy.held:
	case outcome := <-searched:
		t.Fatalf("search X returned before its sweep sent a point in time delete request: %d results, error %v", len(outcome.page.IDs), outcome.err)
	case <-time.After(absentSweepHoldTimeout):
		t.Fatalf("the proxy did not receive a point in time delete request within %s", absentSweepHoldTimeout)
	}
	page, err := integration.TrySearch(harness, absentSweepQuery, "")
	proxy.release()
	return <-searched, searchOutcome{page: page, err: err}
}
