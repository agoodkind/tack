package integration

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

const (
	// sessionIdleTimeout and sessionAbsoluteTimeout are the short session
	// deadlines that the expiry tests wait out in real time.
	sessionIdleTimeout     = 3 * time.Second
	sessionAbsoluteTimeout = 6 * time.Second
	// sessionActivityStep is the real-time gap between two continuations. It
	// stays below the idle deadline.
	sessionActivityStep = time.Second
)

// queryService builds the production search service over the fixture's
// stores and adapter with the wall clock and the given session deadlines.
func queryService(t *testing.T, fixture queryFixture, responseBytes int, idle, absolute time.Duration) *service.SearchQueryService {
	t.Helper()
	settings := config.SearchQuerySettings{
		MaxQueryBytes: 1024, MaxResponseBytes: responseBytes, MaxTokenBytes: 64 << 10, BatchSize: 100, MaxBatches: 4,
		MaxResults: 25, IdleTimeout: idle, AbsoluteTimeout: absolute, RequestDeadline: time.Minute,
		CursorKey: os.Getenv("OPENSEARCH_CURSOR_KEY"),
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("validate settings: %v", err)
	}
	policies := fixture.Stores.SearchPolicySet()
	sessions := fixture.Stores.SearchSessions(clock.Wall{}, settings.IdleTimeout)
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Hour, TokenBytes: settings.MaxTokenBytes, BatchSize: settings.BatchSize})
	return service.NewSearchQueryService(service.SearchQueryPorts{
		Ranker: ranker, Sessions: sessions, Expired: sessions,
		Summaries: fixture.Stores.NodeSummaries(policies), Access: policies,
		Index: fixture.Stores,
	}, clock.Wall{}, settings)
}

// storedSession loads one durable session through the production store.
func storedSession(t *testing.T, fixture queryFixture, sessionID uuid.UUID) searchdomain.Session {
	t.Helper()
	session, err := fixture.Stores.SearchSessions(clock.Wall{}, sessionIdleTimeout).Load(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return session
}

func serviceRequest(t *testing.T, fixture queryFixture, text string) service.SearchRequest {
	t.Helper()
	workspace := fixture.Workspaces[0]
	return service.SearchRequest{
		PrincipalID: workspace.Actors[0].UserID, EntryPointID: entryPoint(t, fixture, workspace),
		MemberOrganizations: []uuid.UUID{workspace.OrgID}, Text: text, NodeType: "", SessionID: uuid.Nil, Version: 0,
	}
}

func continued(request service.SearchRequest, page service.SearchPage) service.SearchRequest {
	request.SessionID, request.Version = page.SessionID, page.NextVersion
	return request
}

// TestSearchSessionDeadlines verifies the idle deadline and the absolute
// deadline in real time. Neither replay nor activity extends the absolute
// deadline. The last continuation renews the idle deadline to the absolute
// deadline, which caps it.
func TestSearchSessionDeadlines(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	putCursorCorpus(t, fixture, "linen compass", 300)
	searcher := queryService(t, fixture, 16384, sessionIdleTimeout, sessionAbsoluteTimeout)
	request := serviceRequest(t, fixture, "linen compass")
	idle, err := searcher.Search(t.Context(), request)
	if err != nil {
		t.Fatalf("open idle session: %v", err)
	}
	waitUntil(t, storedSession(t, fixture, idle.SessionID).IdleDeadline)
	if _, err := searcher.Search(t.Context(), continued(request, idle)); !errors.Is(err, searchdomain.ErrSessionExpired) {
		t.Fatalf("an idle session continued: %v", err)
	}
	page, err := searcher.Search(t.Context(), request)
	if err != nil {
		t.Fatalf("open active session: %v", err)
	}
	absolute := storedSession(t, fixture, page.SessionID).AbsoluteDeadline
	current := continued(request, page)
	continuations := 0
	for clock.Now().Add(2 * sessionActivityStep).Before(absolute) {
		waitUntil(t, clock.Now().Add(sessionActivityStep))
		next, err := searcher.Search(t.Context(), current)
		if err != nil {
			t.Fatalf("continuation %d before the absolute deadline failed: %v", continuations, err)
		}
		replayed, err := searcher.Search(t.Context(), current)
		if err != nil || replayed.NextVersion != next.NextVersion {
			t.Fatalf("replay of continuation %d failed or advanced the session: %v", continuations, err)
		}
		current = continued(request, next)
		continuations++
	}
	waitUntil(t, absolute)
	active := storedSession(t, fixture, page.SessionID)
	if continuations == 0 || !active.AbsoluteDeadline.Equal(absolute) || !active.IdleDeadline.Equal(absolute) {
		t.Fatalf("after %d continuations the idle deadline is %s and the absolute deadline is %s, want both at the absolute deadline %s",
			continuations, active.IdleDeadline, active.AbsoluteDeadline, absolute)
	}
	if _, err := searcher.Search(t.Context(), current); !errors.Is(err, searchdomain.ErrSessionExpired) {
		t.Fatalf("activity extended the session past its absolute deadline: %v", err)
	}
}

// TestSearchExpiredSessionCleanup requires a new search to delete an expired
// session's records. The sweep lists a session after the minute of its idle
// deadline ends. The test waits for the end of that minute.
func TestSearchExpiredSessionCleanup(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	putCursorCorpus(t, fixture, "saddle beacon", 60)
	searcher := queryService(t, fixture, 16384, sessionIdleTimeout, sessionAbsoluteTimeout)
	request := serviceRequest(t, fixture, "saddle beacon")
	page, err := searcher.Search(t.Context(), request)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	deadline := storedSession(t, fixture, page.SessionID).IdleDeadline
	waitUntil(t, deadline.Truncate(time.Minute).Add(time.Minute))
	if _, err := searcher.Search(t.Context(), request); err != nil {
		t.Fatalf("open second session: %v", err)
	}
	_, err = fixture.Stores.SearchSessions(clock.Wall{}, sessionIdleTimeout).Load(t.Context(), page.SessionID)
	if !errors.Is(err, searchdomain.ErrSessionNotFound) {
		t.Fatalf("expired session %s remains after cleanup: %v", page.SessionID, err)
	}
}

// TestSearchByteLimitedResponse stops each page before the first result
// that would exceed the response budget and still returns every node.
func TestSearchByteLimitedResponse(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	entryID := entryPoint(t, fixture, workspace)
	nodes := make([]uuid.UUID, 0, 30)
	for range 30 {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, entryID, strings.Repeat("Tundra ", 40), "tundra relay", "excluded"))
	}
	drainSearchWork(t, fixture.Worker, 500)
	searcher := queryService(t, fixture, 1024, 15*time.Minute, 2*time.Hour)
	request := serviceRequest(t, fixture, "tundra relay")
	returned := make([]uuid.UUID, 0, len(nodes))
	for range 200 {
		page, err := searcher.Search(t.Context(), request)
		if err != nil {
			t.Fatalf("search page: %v", err)
		}
		if len(page.Results) > 2 {
			t.Fatalf("a 1,024-byte page returned %d results", len(page.Results))
		}
		for _, result := range page.Results {
			returned = append(returned, result.ID)
		}
		if page.Complete {
			requireExactlyOnce(t, returned, nodes)
			return
		}
		request = continued(request, page)
	}
	t.Fatal("byte-limited traversal did not complete")
}
