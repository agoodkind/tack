//go:build runtimeaudit

package integration_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const sessionReplayURISuffix = "/replay/"

// TestSearchRuntimeAuditConcurrentPageCommitUncertain opens a session through
// tack_search and continues it with a cursor. A log hook runs a second
// tack_search with the same cursor when the first continuation's page commit
// transaction prepares its writes. The second search commits the page and
// writes the replay record at that session version. The hook then pauses the
// test FoundationDB container. The first continuation's commit times out with
// error 1031, and a second hook resumes the container at the storage failure
// log.
// The second search commit conflicts with the first continuation's session
// header read. The first continuation's commit cannot apply after that conflict.
//
// The test requires the first continuation to return the fixed unavailable
// text and an empty page, and the second search to return results. It checks
// that the first continuation stores a failed read row with code
// search_unavailable and one incomplete session write event with its own
// operation ID, actor, and session target. The first continuation must not
// store change events. The second search must store the replay record change
// under another operation.
func TestSearchRuntimeAuditConcurrentPageCommitUncertain(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, _ := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	opened := integration.CallSearch(t, harness, failureAuditQuery, "")
	if opened.Cursor == "" {
		t.Fatal("first search did not return a cursor")
	}
	session := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor)
	var secondMutex sync.Mutex
	var second integration.SearchPage
	var secondErr error
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		sessionPageCommitReady: func() {
			page, searchErr := integration.TrySearch(harness, failureAuditQuery, opened.Cursor)
			secondMutex.Lock()
			second, secondErr = page, searchErr
			secondMutex.Unlock()
			setContainerPaused(t, container, true)
		},
		retireStorageFailed: func() { setContainerPaused(t, container, false) },
	})
	write := installSessionWriteCapture(sessionPageCommitReady)

	first, firstErr := integration.TrySearch(harness, failureAuditQuery, opened.Cursor)
	hooks.requireFinished(t)

	if firstErr == nil || firstErr.Error() != sessionTimeoutUnavailableText || len(first.IDs) != 0 || first.Cursor != "" {
		t.Fatalf("first continuation returned error %v, result count %d, cursor %q; want error text %q and an empty page", firstErr, len(first.IDs), first.Cursor, sessionTimeoutUnavailableText)
	}
	secondMutex.Lock()
	defer secondMutex.Unlock()
	if secondErr != nil || len(second.IDs) == 0 {
		t.Fatalf("second search returned error %v and result count %d; want results", secondErr, len(second.IDs))
	}
	_, operation, err := write.result()
	if err != nil {
		t.Fatalf("read session and operation for session write log %s: %v", write.message, err)
	}
	replaySuffix := sessionReplayURISuffix + strconv.FormatUint(session.Version, 10)
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(found []audit.Row) bool {
		failed := cleanupLossReads(found, audit.OutcomeFailed)
		return len(failed) == 1 && len(writeUncertainEvents(found, failed[0].Context.OperationID)) == 1 &&
			len(replayChanges(found, session.ID.String(), replaySuffix)) == 1
	})
	read := onlyRow(t, cleanupLossReads(rows, audit.OutcomeFailed), "the first continuation read row")
	if read.Error == nil || read.Error.Code != searchUnavailableCode {
		t.Fatalf("read row = %+v; want code %s", read, searchUnavailableCode)
	}
	event := onlyRow(t, writeUncertainEvents(rows, read.Context.OperationID), "the incomplete session write event")
	requireWriteUncertainEvent(t, event, read, write, audit.VerbSearchSessionUpdate, fixture.Workspaces[0].Actors[0].UserID)
	requireNoChangeRows(t, rows, operation.Context.OperationID)
	replay := onlyRow(t, replayChanges(rows, session.ID.String(), replaySuffix), "the second search replay record change")
	if replay.Context.OperationID == operation.Context.OperationID {
		t.Fatalf("replay record change row %+v uses uncertain write operation %s", replay, operation.Context.OperationID)
	}
}

func replayChanges(rows []audit.Row, sessionID, suffix string) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(audit.VerbSearchSessionCreate) && row.Context.Phase == audit.PhaseChange && target != nil &&
			target.NodeType == audit.TargetSearchSessionReplay && strings.Contains(target.URI, sessionID) && strings.HasSuffix(target.URI, suffix) {
			selected = append(selected, row)
		}
	}
	return selected
}
