package integration_test

import (
	"fmt"
	"testing"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// continueThroughRetirement continues the old session while running worker
// slices until less than one continuation interval remains before its
// absolute deadline. It requires retirement to start, new sessions to use
// target, and the source index state to remain while rejecting replacement
// completion after retirement starts.
func continueThroughRetirement(t *testing.T, fixture parentEdgeFixture, old searchdomain.Session, cursor, target string) string {
	t.Helper()
	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	nextContinuation := clock.Now().Add(openSessionStep)
	retiring := false
	for clock.Now().Add(openSessionStep).Before(old.AbsoluteDeadline) {
		if !clock.Now().Before(nextContinuation) {
			page := searchPageOf(t, fixture, openSessionQuery, cursor)
			if page.cursor == "" || page.results == 0 {
				t.Fatalf("session %s returned %d results and no cursor before its absolute deadline", old.ID, page.results)
			}
			cursor, nextContinuation = page.cursor, clock.Now().Add(openSessionStep)
		}
		current, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if retiring && !found {
			t.Fatalf("the replacement finished while session %s could still continue", old.ID)
		}
		if found && current.State == searchdomain.RebuildRetiring {
			if !retiring {
				requireNewSessionIndex(t, fixture, target)
			}
			retiring = true
			requireRetiringSource(t, fixture, old)
		}
		runSliceOrWait(t, fixture)
	}
	if !retiring {
		t.Fatalf("the replacement did not start retiring %s before the absolute deadline %s", old.Query.Index, old.AbsoluteDeadline)
	}
	return cursor
}

func requireNewSessionIndex(t *testing.T, fixture parentEdgeFixture, target string) {
	t.Helper()
	page := searchPageOf(t, fixture, fmt.Sprintf("%s after the switch", openSessionQuery), "")
	if page.cursor == "" {
		t.Fatalf("a new session after the switch returned %d results and no cursor", page.results)
	}
	session := sessionOfCursor(t, fixture, page.cursor)
	if session.Query.Index != target {
		t.Fatalf("new session %s reads index %s after the switch, want %s", session.ID, session.Query.Index, target)
	}
}

// requireRetiringSource requires the old session's source index to answer a
// settings lookup and retain its retirement time and session presence entry.
func requireRetiringSource(t *testing.T, fixture parentEdgeFixture, old searchdomain.Session) {
	t.Helper()
	source := old.Query.Index
	if _, err := fixture.adapter.IndexSettings(t.Context(), source); err != nil {
		t.Fatalf("read retiring index %s while session %s can continue: %v", source, old.ID, err)
	}
	requireRetiredSince(t, fixture, source, true)
	if presence := presenceSessions(t, fixture, source); !presence[old.ID.String()] {
		t.Fatalf("index %s lists presence of sessions %v, want session %s", source, presence, old.ID)
	}
}

func requireRetiredSince(t *testing.T, fixture parentEdgeFixture, index string, want bool) {
	t.Helper()
	_, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).RetiredSince(t.Context(), index)
	if err != nil {
		t.Fatalf("read retirement time of index %s: %v", index, err)
	}
	if found != want {
		t.Fatalf("index %s has a retirement time: %t, want %t", index, found, want)
	}
}

func presenceSessions(t *testing.T, fixture parentEdgeFixture, index string) map[string]bool {
	t.Helper()
	sessions := make(map[string]bool)
	layout := sessionKeyLayouts[sessionPresentFamily]
	for _, key := range stateKeysOf(readStateKeys(t, fixture), sessionPresentFamily) {
		if key.text(t, presentIndexPosition) == index {
			sessions[key.text(t, layout.session)] = true
		}
	}
	return sessions
}
