package integration_test

import (
	"errors"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	undeletableQuery = "ochre culvert undeletable sweep"
	undeletableNodes = 30
	undeletableIdle  = 3 * time.Second
	undeletableKeys  = 2
)

// TestSearchSweepPassesUndeletableExpiryKeys writes two expiry keys without
// session headers in an earlier minute and expires one real session. Before
// random selection, those keys filled the sweep limit on every search. The test
// runs at most 16 searches until a sweep deletes the real session.
func TestSearchSweepPassesUndeletableExpiryKeys(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", undeletableIdle.String())
	fixture := integration.NewQueryFixture(t)
	putIdleCorpus(t, fixture, undeletableQuery, undeletableNodes)

	opened := integration.CallSearch(t, fixture.Harness, undeletableQuery, "")
	session := integration.CursorSession(t, fixture, opened.Cursor)
	writeExpiryKeys(t, clock.Now().Unix()/secondsPerMinute-expiredBacklogAge, undeletableKeys)
	waitForTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sweepExpiryMargin))

	store := fixture.Stores.SearchSessions(clock.Wall{}, undeletableIdle)
	for attempt := range sweepAttemptLimit {
		integration.CallSearch(t, fixture.Harness, undeletableQuery, "")
		_, err := store.Load(integration.WithTestOperation(t, t.Context()), session.ID)
		if errors.Is(err, searchdomain.ErrSessionNotFound) {
			return
		}
		if err != nil {
			t.Fatalf("load session %s after sweep %d: %v", session.ID, attempt, err)
		}
	}
	t.Fatalf("%d searches did not delete session %s past %d undeletable keys", sweepAttemptLimit, session.ID, undeletableKeys)
}
