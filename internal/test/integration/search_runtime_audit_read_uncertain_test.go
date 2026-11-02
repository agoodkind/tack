//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	sessionCreateCommitReady     = "search.session.create_commit_ready"
	sessionPageCommitReady       = "search.session.page_commit_ready"
	snapshotStepRecorded         = "audit.external.recorded"
	sessionRecordingFailedLog    = "search.session.recording_failed"
	sessionWriteUncertainCode    = "session_write_uncertain"
	sessionWriteUncertainMessage = "the search session write ended without a confirmed commit result"
	searchUnavailableCode        = "search_unavailable"
	auditRecordingFailedCode     = "audit_recording_failed"
)

// TestSearchRuntimeAuditCommitUncertainReadOutcome runs tack_search four times
// with a real engine and the real outbox. In the first case, a log hook
// pauses the test FoundationDB container before the session create transaction
// reads, and the transaction ends without a commit. In the second and third
// cases, the hook pauses the container after the session create transaction or
// the page commit transaction prepares its writes. The commit then times out
// with error 1031. In these three cases, a second hook resumes the container
// at the storage failure log. In the fourth case, the hook pauses the
// container after the session create transaction prepares its writes and
// keeps it paused until the incomplete session write event cannot commit.
// Each search must return the fixed unavailable text and an empty page.
// The read row must store the failed outcome with code search_unavailable in
// the first three cases and audit_recording_failed in the fourth case.
// The second and third cases must each store one incomplete session write
// event. The event must use the operation ID and actor that the write read
// from its context, the read operation as its parent, and the session target
// with the owner. The first case must not store an incomplete session write
// event.
func TestSearchRuntimeAuditCommitUncertainReadOutcome(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, _ := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	orgID := fixture.Workspaces[0].OrgID
	actorID := fixture.Workspaces[0].Actors[0].UserID
	cases := []struct {
		name          string
		pause         string
		resume        string
		code          string
		verb          audit.Verb
		requireAbsent bool
	}{
		{name: "no commit", pause: snapshotStepRecorded, resume: retireStorageFailed, code: searchUnavailableCode, verb: "", requireAbsent: true},
		{name: "create commit", pause: sessionCreateCommitReady, resume: retireStorageFailed, code: searchUnavailableCode, verb: audit.VerbSearchSessionCreate, requireAbsent: false},
		{name: "page commit", pause: sessionPageCommitReady, resume: retireStorageFailed, code: searchUnavailableCode, verb: audit.VerbSearchSessionUpdate, requireAbsent: false},
		{name: "recording loss", pause: sessionCreateCommitReady, resume: sessionRecordingFailedLog, code: auditRecordingFailedCode, verb: "", requireAbsent: false},
	}
	seen := map[uuid.UUID]bool{}
	withoutCommit := []uuid.UUID{}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			hooks := installRetireLogHooks(t, "", map[string]func(){
				current.pause:  func() { setContainerPaused(t, container, true) },
				current.resume: func() { setContainerPaused(t, container, false) },
			})
			write := installSessionWriteCapture(current.pause)
			page, err := integration.TrySearch(harness, failureAuditQuery, "")
			hooks.requireFinished(t)
			if err == nil || err.Error() != sessionTimeoutUnavailableText || len(page.IDs) != 0 || page.Cursor != "" {
				t.Fatalf("tack_search returned error %v, result count %d, cursor %q; want text %q and an empty page", err, len(page.IDs), page.Cursor, sessionTimeoutUnavailableText)
			}
			rows := fixture.audited.waitForRows(t, orgID, func(found []audit.Row) bool {
				reads := unseenSearchReads(found, seen)
				return len(reads) == 1 && (current.verb == "" || len(writeUncertainEvents(found, reads[0].Context.OperationID)) == 1)
			})
			read := onlyRow(t, unseenSearchReads(rows, seen), "the tack_search read row")
			seen[read.EventID] = true
			if read.Outcome != audit.OutcomeFailed || read.Error == nil || read.Error.Code != current.code ||
				strings.Contains(read.Error.Message, transactionTimedOutText) {
				t.Fatalf("read row = %+v; want code %s", read, current.code)
			}
			if current.requireAbsent {
				withoutCommit = append(withoutCommit, read.Context.OperationID)
			}
			if current.verb != "" {
				event := onlyRow(t, writeUncertainEvents(rows, read.Context.OperationID), "the incomplete session write event")
				requireWriteUncertainEvent(t, event, read, write, current.verb, actorID)
			}
		})
	}
	rows := fixture.audited.ledgerRows(t, orgID)
	for _, operationID := range withoutCommit {
		if events := writeUncertainEvents(rows, operationID); len(events) != 0 {
			t.Fatalf("stored %d incomplete session write events for operation %s; want 0", len(events), operationID)
		}
	}
}

func requireWriteUncertainEvent(t *testing.T, event, read audit.Row, write *sessionWriteCapture, verb audit.Verb, actorID uuid.UUID) {
	t.Helper()
	sessionID, operation, err := write.result()
	if err != nil {
		t.Fatalf("read session and operation for session write log %s: %v", write.message, err)
	}
	target := event.Context.Target
	if event.Action != string(verb) || event.Outcome != audit.OutcomeIncomplete || event.Context.Phase != audit.PhaseResult ||
		event.Context.OperationClass != audit.OperationDatabaseChange || event.Error.Message != sessionWriteUncertainMessage ||
		event.Context.OperationID != operation.Context.OperationID || event.Context.ParentOperationID != read.Context.OperationID ||
		event.ActorID != actorID || operation.Actor.ID != actorID || read.ActorID != actorID ||
		event.EntityID != sessionID || target == nil || *target != audit.SearchSessionTarget(sessionID, actorID) {
		t.Fatalf("incomplete session write event: row=%+v target=%+v; want verb=%s operation=%s parent operation=%s actor=%s session=%s",
			event, target, verb, operation.Context.OperationID, read.Context.OperationID, actorID, sessionID)
	}
}

func unseenSearchReads(rows []audit.Row, seen map[uuid.UUID]bool) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.Action == string(audit.VerbNodeSearch) && row.Context.Phase == audit.PhaseRead && !seen[row.EventID] {
			selected = append(selected, row)
		}
	}
	return selected
}

func writeUncertainEvents(rows []audit.Row, readOperationID uuid.UUID) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.Error != nil && row.Error.Code == sessionWriteUncertainCode && row.Context.ParentOperationID == readOperationID {
			selected = append(selected, row)
		}
	}
	return selected
}
