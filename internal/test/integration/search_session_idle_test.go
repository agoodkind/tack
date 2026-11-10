package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// renewalIdle is the session idle timeout the renewal test sets through
	// OPENSEARCH_SESSION_IDLE_TIMEOUT and production uses as the point in time keep-alive.
	renewalIdle = 6 * time.Second
	// renewalGap is the wait from one page's end to the next continuation.
	// It is shorter than renewalIdle.
	renewalGap = 4 * time.Second
	// renewalContinuations is the number of continuations after the first page.
	// With renewalGap between pages, the traversal lasts longer than renewalIdle.
	renewalContinuations = 2
	// renewalNodes fills three pages of 25 results and leaves more matches.
	// The third page returns a cursor.
	renewalNodes = 80
	// renewalQuery is the text that every node in the renewal corpus includes.
	renewalQuery = "amber quay idle renewal"
	// sweepIdle is the session idle timeout for the sweep test.
	sweepIdle = 3 * time.Second
	// sweepNodes exceeds one page of 25 results.
	// The first page returns a cursor.
	sweepNodes = 30
	// sweepQuery is the text that every node in the sweep corpus includes.
	sweepQuery = "russet weir idle sweep"
	// sweepMargin is the wait after the minute containing the idle deadline ends
	// and before the sweep searches start.
	sweepMargin = time.Second
	// sweepSearches limits the number of new searches that may run the expiry sweep.
	sweepSearches = 4
)

// TestSearchIdleDeadlineRenewsSessionAndPointInTime sets the idle timeout to renewalIdle.
// It calls tack_search through a proxy that records engine requests and reads the first page.
// It reads two continuations, each renewalGap after the previous page ends.
// The traversal must last longer than renewalIdle.
// Every page must send pit.keep_alive equal to renewalIdle in every ranked batch and store
// an idle deadline renewalIdle after the page commits.
// After the stored idle deadline passes, a continuation must return the expired cursor answer.
// Tack must send a delete request for the session's point in time.
// The open point in time listing must not contain it.
func TestSearchIdleDeadlineRenewsSessionAndPointInTime(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", renewalIdle.String())
	fixture := integration.NewQueryFixture(t)
	putIdleCorpus(t, fixture, renewalQuery, renewalNodes)
	harness, capture := newCaptureHarness(t, fixture, false)

	opened := clock.Now()
	page, pits := renewedPage(t, fixture, harness, capture, "")
	for range renewalContinuations {
		waitForTime(t, clock.Now().Add(renewalGap))
		next, nextPITs := renewedPage(t, fixture, harness, capture, page.Cursor)
		page = next
		pits = append(pits, nextPITs...)
	}
	if elapsed := clock.Now().Sub(opened); elapsed <= renewalIdle || page.Cursor == "" {
		t.Fatalf("traversal lasted %s and returned cursor %q but must last longer than %s and return a nonempty cursor", elapsed, page.Cursor, renewalIdle)
	}

	session := integration.CursorSession(t, fixture, page.Cursor)
	requirePITOpen(t, fixture, pits)
	waitForTime(t, session.IdleDeadline)
	if _, err := integration.TrySearch(harness, renewalQuery, page.Cursor); err == nil || !strings.Contains(err.Error(), expiredCursorText) {
		t.Fatalf("expired continuation returned %v instead of an error containing %q", err, expiredCursorText)
	}
	requireDeleteSent(t, capture, pits)
	requirePITsClosed(t, fixture, pits)
}

// TestSearchSweepDeletesExpiredPointInTime sets the idle timeout to sweepIdle.
// It opens a session through tack_search and the recording proxy, then leaves its cursor unused.
// After the minute containing the idle deadline ends, it runs new searches to invoke the sweep
// until the session store cannot find the session.
// Tack must send a delete request for the session's point in time.
// The open point in time listing must not contain it.
// OpenSearch can also drop the point in time after its keep-alive expires.
// The recorded delete request shows that the sweep closed it.
func TestSearchSweepDeletesExpiredPointInTime(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", sweepIdle.String())
	fixture := integration.NewQueryFixture(t)
	putIdleCorpus(t, fixture, sweepQuery, sweepNodes)
	harness, capture := newCaptureHarness(t, fixture, false)

	first := integration.CallSearch(t, harness, sweepQuery, "")
	if first.Cursor == "" {
		t.Fatalf("first sweep page returned %d matches without a cursor", sweepNodes)
	}
	pits := capturedPITs(t, capture.take())
	session := integration.CursorSession(t, fixture, first.Cursor)
	waitForTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sweepMargin))
	store := fixture.Stores.SearchSessions(clock.Wall{}, sweepIdle)
	for attempt := range sweepSearches {
		integration.CallSearch(t, harness, fmt.Sprintf("%s sweep %d", sweepQuery, attempt), "")
		_, err := store.Load(integration.WithTestOperation(t, t.Context()), session.ID)
		if errors.Is(err, searchdomain.ErrSessionNotFound) {
			requireDeleteSent(t, capture, pits)
			requirePITsClosed(t, fixture, pits)
			return
		}
	}
	t.Fatalf("session store still finds a session after %d sweep searches: %s", sweepSearches, session.ID)
}

// renewedPage calls tack_search for renewalQuery with cursor and requires the call to succeed.
// It requires every ranked batch request to send pit.keep_alive equal to renewalIdle.
// If the page returns a cursor, it requires the stored idle deadline to fall between
// the call start plus renewalIdle and the call end plus renewalIdle.
// It returns the page and the point in time IDs of its ranked batches.
func renewedPage(t *testing.T, fixture integration.QueryFixture, harness *integration.MCPHarness, capture *searchCapture, cursor string) (integration.SearchPage, []string) {
	t.Helper()
	started := clock.Now()
	page, err := integration.TrySearch(harness, renewalQuery, cursor)
	finished := clock.Now()
	if err != nil {
		t.Fatalf("tack_search failed for cursor %q: %v", cursor, err)
	}
	searches := capture.take()
	keepAlive := strconv.FormatInt(renewalIdle.Milliseconds(), 10) + "ms"
	for _, search := range searches {
		var body rankedRequestBody
		if decodeErr := json.Unmarshal(search.Request, &body); decodeErr != nil || body.PIT.KeepAlive != keepAlive {
			t.Fatalf("ranked batch sent pit.keep_alive %q with decode error %v but must send %q", body.PIT.KeepAlive, decodeErr, keepAlive)
		}
	}
	if page.Cursor != "" {
		session := integration.CursorSession(t, fixture, page.Cursor)
		if session.IdleDeadline.Before(started.Add(renewalIdle)) || session.IdleDeadline.After(finished.Add(renewalIdle)) {
			t.Fatalf("session %s stored idle deadline %s outside the window from %s to %s after adding idle timeout %s to both call times",
				session.ID, session.IdleDeadline, started, finished, renewalIdle)
		}
	}
	return page, capturedPITs(t, searches)
}

// putIdleCorpus creates count nodes of a new opaque node type under the first workspace's
// entry point, with query as included text.
// It runs search worker slices until indexing finishes.
func putIdleCorpus(t *testing.T, fixture integration.QueryFixture, query string, count int) {
	t.Helper()
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range count {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Idle node %d", number), query, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, fullPageSlicesPerNode*count+fullPageFixtureSlices)
}
