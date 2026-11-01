package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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
	// sweepFailedMessage is the error record of a failed expiry sweep.
	sweepFailedMessage = "search.session.sweep_failed"
	// sweepSkippedMessage is the debug record of a session that another
	// sweep deleted.
	sweepSkippedMessage = "search.session.sweep_skipped"
)

// sweepLogRecord is one JSON log record of the search service.
type sweepLogRecord struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Err     string `json:"err"`
}

// lockedLog is a log destination that concurrent searches share.
type lockedLog struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (l *lockedLog) Write(record []byte) (int, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	written, err := l.buffer.Write(record)
	if err != nil {
		return written, fmt.Errorf("write a log record: %w", err)
	}
	return written, nil
}

func (l *lockedLog) contents() []byte {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return bytes.Clone(l.buffer.Bytes())
}

// searchOutcome is the page or the failure of one tack_search call.
type searchOutcome struct {
	page integration.SearchPage
	err  error
}

// armDelete starts keeping the first point in time delete request open.
func (proxy *predictHoldProxy) armDelete() {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.deleteArmed = true
}

// keepsOpen reports whether the proxy keeps request open until release.
func (proxy *predictHoldProxy) keepsOpen(request *http.Request) bool {
	if request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, pitDeletePath) {
		return proxy.holdDelete()
	}
	return strings.HasPrefix(request.URL.Path, predictPathPrefix) && proxy.hold(proxy.matches(request))
}

// holdDelete reports whether the proxy keeps a point in time delete request
// open. Only the first one after armDelete stays open.
func (proxy *predictHoldProxy) holdDelete() bool {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if !proxy.deleteArmed || proxy.holding {
		return false
	}
	proxy.holding = true
	close(proxy.held)
	return true
}

// TestSearchSweepSkipsSessionAnotherSweepDeleted opens two sessions through
// tack_search and waits past their expiry minute. The proxy keeps the delete
// request of search X open while its sweep closes the first listed session.
// Search Y then lists the same sessions and deletes both. After release, the
// sweep of X loads the second session, which no longer exists. Both searches
// must return results, both sessions must be gone, and the sweep of X must
// log one debug skip record and no error record.
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

	logged := &lockedLog{mutex: sync.Mutex{}, buffer: bytes.Buffer{}}
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	first, second := interleaveSweeps(t, harness, proxy)
	slog.SetDefault(previous)

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

// requireSweepSkip requires logged to contain one debug record named
// search.session.sweep_skipped and no error record named
// search.session.sweep_failed.
func requireSweepSkip(t *testing.T, logged []byte) {
	t.Helper()
	skipped := 0
	decoder := json.NewDecoder(bytes.NewReader(logged))
	for {
		var record sweepLogRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode a search log record: %v", err)
		}
		if record.Level == slog.LevelError.String() && record.Message == sweepFailedMessage {
			t.Errorf("the sweep logged %s %s with error %q", record.Level, record.Message, record.Err)
		}
		if record.Level == slog.LevelDebug.String() && record.Message == sweepSkippedMessage {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("the sweeps logged %d %s records, want 1", skipped, sweepSkippedMessage)
	}
}
