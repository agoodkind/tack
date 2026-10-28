package integration_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	sessionHeaderFamily  = "search_session"
	sessionChildFamily   = "search_session_child"
	sessionExpiryFamily  = "search_session_expiry"
	sessionPresentFamily = "search_session_present"
	sessionVersionFamily = "search_session_version"
	expiryMinutePosition = 1
	presentIndexPosition = 1
	sweepAttemptLimit    = 16
	sweepExpiryMargin    = time.Second
	secondsPerMinute     = int64(time.Minute / time.Second)
)

type sessionKeyLayout struct {
	bucket, session int
}

// sessionKeyLayouts maps each supported session key family to the tuple
// positions of its bucket and session ID.
var sessionKeyLayouts = map[string]sessionKeyLayout{
	sessionHeaderFamily:  {bucket: 1, session: 2},
	sessionChildFamily:   {bucket: 1, session: 2},
	sessionExpiryFamily:  {bucket: 2, session: 3},
	sessionPresentFamily: {bucket: 2, session: 3},
	sessionVersionFamily: {bucket: 4, session: 5},
}

type statePage struct {
	ids    []uuid.UUID
	cursor string
}

// trySearchPage calls tack_search through the MCP driver and returns stand-in
// result IDs and the next cursor. It returns an error when the call or
// response parsing fails.
func trySearchPage(t *testing.T, fixture parentEdgeFixture, query, cursor string) (statePage, error) {
	t.Helper()
	result, err := fixture.driver.Call(t.Context(), fixture.first.Actors[0].Token, "tack_search", datagen.ToolArguments{
		WorkspaceReference: fixture.first.Slug, Query: query, Cursor: cursor,
	})
	if err != nil {
		return statePage{}, fmt.Errorf("search %q: %w", query, err)
	}
	// The session checks count printed items without resolving fixture node IDs.
	parsed, err := integration.ParseSearchPage(result.Text(), datagen.NewSearchTitles())
	if err != nil {
		return statePage{}, fmt.Errorf("parse search %q: %w", query, err)
	}
	return statePage{ids: parsed.IDs, cursor: parsed.Cursor}, nil
}

func searchPageOf(t *testing.T, fixture parentEdgeFixture, query, cursor string) statePage {
	t.Helper()
	page, err := trySearchPage(t, fixture, query, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// sessionOfCursor decodes cursor with OPENSEARCH_CURSOR_KEY and loads its
// stored search session. It fails the test if decoding or loading fails.
func sessionOfCursor(t *testing.T, fixture parentEdgeFixture, cursor string) searchdomain.Session {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OPENSEARCH_CURSOR_KEY"))
	if err != nil {
		t.Fatalf("decode cursor key: %v", err)
	}
	sessionID, _, err := tools.NewSearchCursorCodec(key).Decode(cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	session, err := fixture.stores.SearchSessions(clock.Wall{}, time.Minute).Load(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return session
}

func storedSessionIDs(t *testing.T, keys []stateKey) map[string]bool {
	t.Helper()
	sessions := make(map[string]bool)
	layout := sessionKeyLayouts[sessionHeaderFamily]
	for _, key := range stateKeysOf(keys, sessionHeaderFamily) {
		sessions[key.text(t, layout.session)] = true
	}
	return sessions
}

// sweepExpiredSessions waits until the latest stored expiry minute ends,
// then opens searches until keys stop referencing the original session IDs.
// It returns the remaining session IDs or fails if the attempt limit expires
// before cleanup finishes.
func sweepExpiredSessions(t *testing.T, fixture parentEdgeFixture, query string) map[string]bool {
	t.Helper()
	keys := readStateKeys(t, fixture)
	expired := storedSessionIDs(t, keys)
	latest, found := int64(0), false
	for _, key := range stateKeysOf(keys, sessionExpiryFamily) {
		latest, found = max(latest, key.integer(t, expiryMinutePosition)), true
	}
	if found {
		waitUntilTime(t, time.Unix((latest+1)*secondsPerMinute, 0).Add(sweepExpiryMargin))
	}
	for attempt := range sweepAttemptLimit {
		searchPageOf(t, fixture, fmt.Sprintf("%s sweep %d", query, attempt), "")
		keys = readStateKeys(t, fixture)
		if !anyStateKeyReferences(keys, expired) {
			return storedSessionIDs(t, keys)
		}
	}
	t.Fatalf("%d new searches left keys of the %d expired sessions", sweepAttemptLimit, len(expired))
	return nil
}

func anyStateKeyReferences(keys []stateKey, identifiers map[string]bool) bool {
	for _, key := range keys {
		if key.references(identifiers) {
			return true
		}
	}
	return false
}

// waitUntilTime waits until instant and fails the test if its context ends
// first.
func waitUntilTime(t *testing.T, instant time.Time) {
	t.Helper()
	select {
	case <-t.Context().Done():
		t.Fatalf("the test ended before %s", instant.UTC().Format(time.RFC3339Nano))
	case <-time.After(instant.Sub(clock.Now())):
	}
}
