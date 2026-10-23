package integration_test

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

const (
	// acceptanceIdleTimeout sets the required session idle timeout to 15
	// minutes.
	acceptanceIdleTimeout = 15 * time.Minute
	// acceptanceAbsoluteTimeout sets the required session absolute timeout to
	// 2 hours.
	acceptanceAbsoluteTimeout = 2 * time.Hour
	// defaultDeadlineQuery is the query text for the session deadline corpus.
	defaultDeadlineQuery = "default deadline compass"
	// defaultDeadlineNodes sets the corpus size to 30 nodes, exceeding the
	// page size of 25 results.
	defaultDeadlineNodes = 30
)

// TestSearchSessionDefaultDeadlines verifies that empty timeout environment
// variables select the 15 minute idle and 2 hour absolute defaults in
// production configuration, query settings, and stored tack_search session
// deadlines.
func TestSearchSessionDefaultDeadlines(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "")
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", "")
	fixture := newRolloutFixture(t)
	settings, err := config.LoadSearchQuerySettings(rolloutContext(t))
	if err != nil {
		t.Fatalf("cannot load search query settings: %v", err)
	}
	if settings.IdleTimeout != acceptanceIdleTimeout || settings.AbsoluteTimeout != acceptanceAbsoluteTimeout {
		t.Fatalf("default session timeouts are %s idle and %s absolute instead of %s and %s",
			settings.IdleTimeout, settings.AbsoluteTimeout, acceptanceIdleTimeout, acceptanceAbsoluteTimeout)
	}
	workspace := fixture.workspaces[0]
	putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, workspace.OrgID), rolloutEntryPoint(t, fixture, workspace), defaultDeadlineQuery, defaultDeadlineNodes)
	drainRolloutWork(t, fixture)

	started := clock.Now()
	page := searchAs(t, fixture, workspace, defaultDeadlineQuery, "")
	finished := clock.Now()
	if page.complete {
		t.Fatalf("search for %q completed in one page", defaultDeadlineQuery)
	}
	session := cursorSessionRecord(t, fixture, page.cursor)
	if session.CreatedAt.Before(started) || session.CreatedAt.After(finished) {
		t.Fatalf("session %s was created at %s, outside the call from %s to %s", session.ID, session.CreatedAt, started, finished)
	}
	if !session.AbsoluteDeadline.Equal(session.CreatedAt.Add(acceptanceAbsoluteTimeout)) {
		t.Fatalf("session %s stores absolute deadline %s for creation time %s", session.ID, session.AbsoluteDeadline, session.CreatedAt)
	}
	if session.IdleDeadline.Before(session.CreatedAt.Add(acceptanceIdleTimeout)) || session.IdleDeadline.After(finished.Add(acceptanceIdleTimeout)) {
		t.Fatalf("session %s stores idle deadline %s for creation time %s and call end %s", session.ID, session.IdleDeadline, session.CreatedAt, finished)
	}
}
