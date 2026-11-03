//go:build runtimeaudit

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sweepCleanupOperation is the log record of one committed cleanup slice.
	sweepCleanupOperation = "store.search_session.cleanup"
	// sweepBeginCleanupOperation identifies the log record for a completed BeginCleanup call.
	sweepBeginCleanupOperation = "store.search_session.begin_cleanup"
	// sweepCloseFailedMessage is the log record of a failed close step.
	sweepCloseFailedMessage = "search.session.close_failed"
	// sweepExtraVisited exceeds the 100 child records of one sweep slice.
	sweepExtraVisited = 150
)

// TestSearchRuntimeAuditSweepLaterSliceAfterContextEnd checks that a search
// returns its page and leaves an expired session closing when its context
// ends after the first slice commits. A later search deletes the session
// without recording a failed cleanup row.
func TestSearchRuntimeAuditSweepLaterSliceAfterContextEnd(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(t, fixture.QueryFixture, failureAuditResponseBytes, sweepProofIdle, sweepProofAbsolute)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	ctx := integration.WithTestOperation(t, t.Context())
	first, err := searcher.Search(ctx, request)
	if err != nil {
		t.Fatalf("open the session: %v", err)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, sweepProofIdle)
	session, err := store.Load(ctx, first.SessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", first.SessionID, err)
	}
	commit := searchdomain.PageCommit{
		Sort: json.RawMessage(`[1]`), PITID: session.Snapshot.PITID,
		Visited: newIDs(sweepExtraVisited), ResultIDs: newIDs(1), Complete: false,
	}
	committed, err := store.CommitPage(ctx, session.ID, session.Version, commit)
	if err != nil {
		t.Fatalf("add visited records to session %s: %v", session.ID, err)
	}
	waitUntilTime(t, committed.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))

	searchThroughEndedSweep(t, searcher, request, map[string]func(){sweepStoppedMessage: func() {}}, sweepCleanupOperation)
	after, err := store.Load(ctx, session.ID)
	if err != nil || !after.Closing {
		t.Fatalf("session %s after the ended sweep = %+v, %v, want a closing session", session.ID, after, err)
	}

	if _, err := searcher.Search(integration.WithTestOperation(t, t.Context()), request); err != nil {
		t.Fatalf("the search that finishes the delete of session %s failed: %v", session.ID, err)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(sessionChanges(current, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1
	})
	for _, code := range []searchdomain.CleanupStep{cleanupNotStarted, searchdomain.CleanupClose, searchdomain.CleanupDelete} {
		if failed := cleanupFailures(rows, session.ID, code); len(failed) != 0 {
			t.Fatalf("session %s has %d %s rows, want 0", session.ID, len(failed), code)
		}
	}
}

// TestSearchRuntimeAuditSweepFirstSliceAfterContextEnd checks the search result
// and audit record when cancellation prevents the first cleanup attempt.
func TestSearchRuntimeAuditSweepFirstSliceAfterContextEnd(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(t, fixture.QueryFixture, failureAuditResponseBytes, failureAuditIdle, failureAuditAbsolute)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	expired := fixture.openExpired(t, searcher, request)

	operationID := searchThroughEndedSweep(t, searcher, request, map[string]func(){}, sweepBeginCleanupOperation)
	store := fixture.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	after, err := store.Load(integration.WithTestOperation(t, t.Context()), expired.ID)
	if err != nil || !after.Closing {
		t.Fatalf("session %s after the ended sweep = %+v, %v, want a closing session", expired.ID, after, err)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupFailures(current, expired.ID, cleanupNotStarted)) == 1
	})
	row := onlyRow(t, cleanupFailures(rows, expired.ID, cleanupNotStarted), "the not started delete")
	requireCleanupFailureRow(t, row, audit.VerbSearchSessionDelete, expired.PrincipalID, operationID)
}

func searchThroughEndedSweep(t *testing.T, searcher *service.SearchQueryService, request service.SearchRequest, actions map[string]func(), cancelAt string) uuid.UUID {
	t.Helper()
	sweeping, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	defer cancel()
	actions[cancelAt] = cancel
	hooks := installRetireLogHooks(t, "", actions)
	page, err := searcher.Search(sweeping, request)
	hooks.requireFinished(t)
	if err != nil || len(page.Results) == 0 {
		t.Fatalf("the search with the ended sweep returned %d results and error %v", len(page.Results), err)
	}
	return operationIDOf(t, sweeping)
}

// TestSearchRuntimeAuditNotStartedCloseRecordingLossRefusesPage checks that
// Search withholds the page when the close row write fails after BeginCleanup
// makes no attempt. It requires the unavailable text, a failed read row with
// code audit_recording_failed, and an open session.
func TestSearchRuntimeAuditNotStartedCloseRecordingLossRefusesPage(t *testing.T) {
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
	opened := integration.CallSearch(t, harness, failureAuditQuery, "")
	expired := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor)
	waitUntilTime(t, expired.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))

	sessionID := expired.ID.String()
	gate := newCleanupLossGate(t, testenv.FoundationDBContainer(testenv.FoundationDB(t)),
		cleanupLossSessionLog(sweepCloseFailedMessage, sessionID), cleanupLossSessionLog(cleanupLossAuditMessage, sessionID))
	gate.install()
	proxy.armDelete()
	page, err := integration.TrySearch(harness, failureAuditQuery, "")
	proxy.release()

	requireCleanupLossRefusal(t, gate, page, err, []string{"record the failed cleanup step", "FoundationDB error code 1031"}, nil)
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
	})
	refused := onlyRow(t, cleanupLossReads(rows, audit.OutcomeFailed), "the refused read")
	if refused.Error == nil || refused.Error.Code != auditRecordingFailedCode {
		t.Fatalf("the failed read event did not store the audit_recording_failed code: %+v", refused)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	after, err := store.Load(integration.WithTestOperation(t, t.Context()), expired.ID)
	if err != nil || after.Closing {
		t.Fatalf("session %s after the lost close row = %+v, %v, want an open session", expired.ID, after, err)
	}
}
