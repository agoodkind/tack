//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireRowWindow = time.Hour
	retireRowPoll   = 200 * time.Millisecond
)

// TestSearchSessionAuditRetireCodesSurviveEitherOrder covers both arrival
// orders of the attempt failure event and the recorder event. Each subtest
// pauses FoundationDB before the failure event write. The write and the
// failure code save both fail. The test cannot force a commit to apply
// after a timeout. The test sends the attempt failure event to the real
// Kafka topic to produce the row for an applied write. One subtest stores
// that row before production code sends the recorder event. The other
// subtest stores it after the recorder row. Each subtest checks audit.events
// for two failed rows with the same actor, operation ID, and start time.
// The row with the attempt event ID has zero failure codes. The other row
// has both codes. Each subtest checks that the call does not return
// audit.ErrEventNotRecorded.
func TestSearchSessionAuditRetireCodesSurviveEitherOrder(t *testing.T) {
	cases := []struct {
		index           string
		firstWriteFirst bool
	}{
		{index: "node-pages-first-write-first", firstWriteFirst: true},
		{index: "node-pages-recorder-first", firstWriteFirst: false},
	}
	for _, current := range cases {
		t.Run(current.index, func(t *testing.T) {
			stores := sessionStoreStores(t)
			stack := startRuntimeAuditStack(t)
			rebuilds := stores.SearchRebuilds(clock.Wall{}).WithFailureRecorder(stack.recorder)
			container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
			beginRetiringRebuild(t, stores, current.index)
			session := retiringSession(t, stores, current.index)
			var written audit.Event
			hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
				retireCleanupFailedMessage: func() {
					written = attemptFailureEvent(t)
					setContainerPaused(t, container, true)
				},
				retireCodesFailedMessage: func() {
					setContainerPaused(t, container, false)
					if current.firstWriteFirst {
						stack.record(t, written)
						stack.waitForRow(t, written.EventID)
					}
				},
			})

			_, err := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), current.index, retireSliceLimit)
			hooks.requireFinished(t)
			requireChangeEventFailure(t, "RetireIndexSessions of "+current.index, err)
			if errors.Is(err, audit.ErrEventNotRecorded) {
				t.Fatalf("RetireIndexSessions of %s with a recorded failure = %v, want no %v", current.index, err, audit.ErrEventNotRecorded)
			}
			if !current.firstWriteFirst {
				waitForAttemptRows(t, stack, written, 1)
				stack.record(t, written)
			}
			requireAttemptRows(t, waitForAttemptRows(t, stack, written, 2), written)
		})
	}
}

// TestSearchSessionAuditRetireRecordsCodesAfterRecovery pauses FoundationDB
// before the failure event write and resumes it after the write fails. A second
// RetireIndexSessions call runs after the attempt lease and recovers the
// attempt. The failure code save does not find the record. The test checks
// that the first call sends the failed event to the audit recorder, audit.events
// contains the failed row with both failure codes, and the first call does not
// return audit.ErrEventNotRecorded.
func TestSearchSessionAuditRetireRecordsCodesAfterRecovery(t *testing.T) {
	stores := sessionStoreStores(t)
	stack := startRuntimeAuditStack(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-codes-after-recovery"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	var written audit.Event
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() {
			written = attemptFailureEvent(t)
			setContainerPaused(t, container, true)
		},
		retireAuditFailedMessage: func() {
			setContainerPaused(t, container, false)
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err != nil {
				t.Fatalf("recover the attempt of index %s: %v", index, err)
			}
		},
	})

	_, err := rebuilds.WithFailureRecorder(stack.recorder).RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit)
	hooks.requireFinished(t)
	requireChangeEventFailure(t, "RetireIndexSessions of "+index, err)
	if errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with a recorded failure = %v, want no %v", index, err, audit.ErrEventNotRecorded)
	}
	row := waitForAttemptRows(t, stack, written, 1)[0]
	requireFailedRowCodes(t, row)
}

func attemptFailureEvent(t *testing.T) audit.Event {
	t.Helper()
	var record struct {
		Event audit.Event `json:"event"`
	}
	if err := json.Unmarshal(retireFamilyValue(t, retireAttemptFamily), &record); err != nil {
		t.Fatalf("decode the attempt record: %v", err)
	}
	event := record.Event
	event.OccurredAt, event.Context.CompletedAt = event.Context.StartedAt, event.Context.StartedAt
	return event
}

func waitForAttemptRows(t *testing.T, stack runtimeAuditStack, event audit.Event, count int) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	found := 0
	for clock.Now().Before(deadline) {
		rows, err := stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: event.Context.OrgID, Oldest: event.Context.StartedAt.Add(-retireRowWindow), Latest: event.Context.StartedAt.Add(retireRowWindow),
			Action: event.Verb, ActorID: event.Actor.ID, EntityID: event.Entity.ID, RequestID: "", TraceID: "", Limit: count + 1,
		})
		if err != nil {
			t.Fatalf("query the rows of attempt event %s: %v", event.EventID, err)
		}
		found = len(rows)
		if found > count {
			t.Fatalf("attempt event %s has %d rows, want %d", event.EventID, found, count)
		}
		if found == count {
			return rows
		}
		time.Sleep(retireRowPoll)
	}
	t.Fatalf("attempt event %s has %d rows after %s, want %d", event.EventID, found, runtimeAuditDeadline, count)
	return nil
}

func requireAttemptRows(t *testing.T, rows []audit.Row, event audit.Event) {
	t.Helper()
	withCodes := 0
	for _, row := range rows {
		if row.Context.OperationID != event.Context.OperationID || row.ActorID != event.Actor.ID ||
			!row.Context.StartedAt.Equal(event.Context.StartedAt) {
			t.Fatalf("row %s has operation %s, actor %s, and start %s, want %s, %s, and %s", row.EventID, row.Context.OperationID,
				row.ActorID, row.Context.StartedAt, event.Context.OperationID, event.Actor.ID, event.Context.StartedAt)
		}
		if row.EventID == event.EventID {
			var evidence retireFailureEvidence
			if len(row.Extra) != 0 {
				if err := json.Unmarshal(row.Extra, &evidence); err != nil {
					t.Fatalf("decode the evidence of row %s: %v", row.EventID, err)
				}
			}
			if row.Outcome != audit.OutcomeFailed || evidence.Cleanup != "" || evidence.Recording != "" {
				t.Fatalf("row %s has outcome %s and evidence %s, want %s and zero failure codes", row.EventID, row.Outcome, row.Extra, audit.OutcomeFailed)
			}
			continue
		}
		requireFailedRowCodes(t, row)
		withCodes++
	}
	if withCodes != 1 {
		t.Fatalf("attempt event %s has %d rows with failure codes, want 1", event.EventID, withCodes)
	}
}

func requireFailedRowCodes(t *testing.T, row audit.Row) {
	t.Helper()
	var evidence retireFailureEvidence
	if err := json.Unmarshal(row.Extra, &evidence); err != nil {
		t.Fatalf("decode the evidence of row %s: %v", row.EventID, err)
	}
	if row.Outcome != audit.OutcomeFailed || evidence.Cleanup != unclassifiedFailureCode || evidence.Recording != transactionTimedOutCode {
		t.Fatalf("row %s has outcome %s and evidence %s, want %s with cleanup code %q and recording code %q",
			row.EventID, row.Outcome, row.Extra, audit.OutcomeFailed, unclassifiedFailureCode, transactionTimedOutCode)
	}
}
