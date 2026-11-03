//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// sweepStopSessions is the number of sessions that expire before the
	// sweep runs.
	sweepStopSessions = 2
	// sweepStopRequestTimeout outlasts the search query deadline. The query
	// deadline ends the open delete request.
	sweepStopRequestTimeout = "40s"
	// sweepStoppedMessage is the record of a sweep that stops before a session.
	sweepStoppedMessage = "search.session.sweep_stopped"
	// sweepStopNotStartedRows counts the close and delete rows of the session
	// that the deadline interrupts.
	sweepStopNotStartedRows = 2
)

// TestSearchRuntimeAuditSweepStopsAfterRequestDeadline checks that a sweep does
// not record a failed delete for a session after the request deadline ends.
// A later search must delete both sessions.
func TestSearchRuntimeAuditSweepStopsAfterRequestDeadline(t *testing.T) {
	setShortSessions(t)
	t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", sweepStopRequestTimeout)
	fixture := newFailureFixture(t)
	harness, proxy := newPredictHoldHarness(t, fixture.QueryFixture)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	sessions := make([]searchdomain.Session, 0, sweepStopSessions)
	for range sweepStopSessions {
		opened := integration.CallSearch(t, harness, failureAuditQuery, "")
		sessions = append(sessions, integration.CursorSession(t, fixture.QueryFixture, opened.Cursor))
	}
	latest := sessions[len(sessions)-1].IdleDeadline
	waitUntilTime(t, latest.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))

	proxy.armDelete()
	logged, restoreLog := captureSweepLog(t)
	page, err := integration.TrySearch(harness, failureAuditQuery, "")
	restoreLog()
	if err != nil || len(page.IDs) == 0 {
		t.Fatalf("the search with the stopped sweep returned %d results and error %v", len(page.IDs), err)
	}
	if !bytes.Contains(logged.contents(), []byte(sweepStoppedMessage)) {
		t.Fatalf("the sweep did not log %s", sweepStoppedMessage)
	}
	proxy.release()
	if _, err := integration.TrySearch(harness, failureAuditQuery, ""); err != nil {
		t.Fatalf("the search that sweeps the remaining sessions failed: %v", err)
	}
	orgID := fixture.Workspaces[0].OrgID
	rows := fixture.audited.waitForRows(t, orgID, func(current []audit.Row) bool {
		notStarted := 0
		for _, session := range sessions {
			if len(sessionChanges(current, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) != 1 {
				return false
			}
			notStarted += len(cleanupFailures(current, session.ID, cleanupNotStarted))
		}
		return notStarted == sweepStopNotStartedRows
	})
	for _, session := range sessions {
		if len(cleanupFailures(rows, session.ID, searchdomain.CleanupClose))+len(cleanupFailures(rows, session.ID, searchdomain.CleanupDelete)) != 0 {
			t.Fatalf("session %s has failed cleanup rows for steps that did not start", session.ID)
		}
		notStarted := cleanupFailures(rows, session.ID, cleanupNotStarted)
		if count := len(notStarted); count != 0 && count != sweepStopNotStartedRows {
			t.Fatalf("session %s has %d not started rows, want 0 or %d", session.ID, count, sweepStopNotStartedRows)
		}
		for _, row := range notStarted {
			if row.Outcome != audit.OutcomeFailed || row.Error.Message == "" {
				t.Fatalf("the not started row of session %s = %+v, want the failed outcome and a message", session.ID, row)
			}
		}
	}
}
