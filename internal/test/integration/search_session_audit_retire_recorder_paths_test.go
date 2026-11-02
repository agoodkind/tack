//go:build runtimeaudit

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireFailureCommitReady  = "search.session.retire_failure_commit_ready"
	uncertainRecordingCode    = "foundationdb_1031_commit_uncertain"
	unreachableBroker         = "localhost:1"
	unreachableProduceTimeout = 2 * time.Second
)

// TestSearchSessionAuditRetireRecordsCodesAfterRecovery pauses FoundationDB
// before the failure event write and resumes it after the write fails. A
// second RetireIndexSessions call runs after the attempt lease and recovers
// the attempt. The failure code save does not find the record. The
// production relay delivers the outbox events. The test requires two
// audit.events rows under the first operation. The failed row has both
// failure codes. The incomplete row has cleanup_result_not_saved and zero
// failure codes. The first call must not return audit.ErrEventNotRecorded.
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
	requireRecordedFailureCauses(t, index, err)
	startRetireRelay(t, stack)
	rows := waitForAttemptRows(t, stack, written, 2)
	failed, incomplete := 0, 0
	for _, row := range rows {
		if row.Outcome == audit.OutcomeFailed {
			requireFailedRowCodes(t, row, transactionTimedOutCode)
			failed++
			continue
		}
		var evidence retireFailureEvidence
		if len(row.Extra) != 0 {
			if err := json.Unmarshal(row.Extra, &evidence); err != nil {
				t.Fatalf("decode the evidence of row %s: %v", row.EventID, err)
			}
		}
		if row.Outcome == audit.OutcomeIncomplete && row.Error != nil && row.Error.Code == retireResultNotSaved && evidence.Cleanup == "" && evidence.Recording == "" {
			incomplete++
		}
	}
	if failed != 1 || incomplete != 1 {
		t.Fatalf("attempt event %s has rows %+v, want 1 failed and 1 incomplete", written.EventID, rows)
	}
}

// TestSearchSessionAuditRetireRecorderIgnoresCancelledCaller pauses
// FoundationDB before the failure event write. After the failure code save
// fails, the test resumes FoundationDB and cancels the caller context.
// The test requires an audit.events row with both failure codes and an
// error without audit.ErrEventNotRecorded. The row must not contain raw
// error text.
func TestSearchSessionAuditRetireRecorderIgnoresCancelledCaller(t *testing.T) {
	stores := sessionStoreStores(t)
	stack := startRuntimeAuditStack(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{}).WithFailureRecorder(stack.recorder)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-cancelled-caller"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	callerContext, cancelCaller := context.WithCancel(t.Context())
	defer cancelCaller()
	var written audit.Event
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() {
			written = attemptFailureEvent(t)
			setContainerPaused(t, container, true)
		},
		retireCodesFailedMessage: func() {
			setContainerPaused(t, container, false)
			cancelCaller()
		},
	})

	_, err := rebuilds.RetireIndexSessions(withFutureOperation(t, callerContext), index, retireSliceLimit)
	hooks.requireFinished(t)
	requireRecordedFailureCauses(t, index, err)
	requireFailedRowCodes(t, waitForAttemptRows(t, stack, written, 1)[0], transactionTimedOutCode)
}

// TestSearchSessionAuditRetireRecorderEventSpills uses a SpillRecorder
// with a real Kafka recorder configured for an unreachable broker. After
// the failure code save fails, the test resumes FoundationDB. The Kafka
// send fails, and the spill appends the event to the FoundationDB outbox.
// The production relay then delivers the event. The test requires an
// audit.events row with both failure codes and an error without
// audit.ErrEventNotRecorded.
func TestSearchSessionAuditRetireRecorderEventSpills(t *testing.T) {
	stores := sessionStoreStores(t)
	stack := startRuntimeAuditStack(t)
	primary, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{unreachableBroker}, Topic: stack.topic, ClientID: retireRecorderClientID, ProduceTimeout: unreachableProduceTimeout,
	})
	if err != nil {
		t.Fatalf("open the Kafka recorder: %v", err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	recorder := audit.CanonicalRecorder{Inner: &audit.SpillRecorder{Primary: primary, Spill: stores.OpsOutbox}}
	rebuilds := stores.SearchRebuilds(clock.Wall{}).WithFailureRecorder(recorder)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-recorder-spill"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	var written audit.Event
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() {
			written = attemptFailureEvent(t)
			setContainerPaused(t, container, true)
		},
		retireCodesFailedMessage: func() { setContainerPaused(t, container, false) },
	})

	_, err = rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit)
	hooks.requireFinished(t)
	requireRecordedFailureCauses(t, index, err)
	startRetireRelay(t, stack)
	requireFailedRowCodes(t, waitForAttemptRows(t, stack, written, 1)[0], transactionTimedOutCode)
}

// TestSearchSessionAuditRetireRecordingCodeMarksUncertainCommit pauses
// FoundationDB after the failure event transaction prepares its writes.
// The commit times out with error 1031, and the failure code save fails.
// The test requires an audit.events row with the recording code
// foundationdb_1031_commit_uncertain. The test cannot choose whether the
// timed-out commit applies. The test does not run a relay, and the outbox
// event does not appear in audit.events.
func TestSearchSessionAuditRetireRecordingCodeMarksUncertainCommit(t *testing.T) {
	stores := sessionStoreStores(t)
	stack := startRuntimeAuditStack(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{}).WithFailureRecorder(stack.recorder)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-uncertain-recording"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	var written audit.Event
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() { written = attemptFailureEvent(t) },
		retireFailureCommitReady:   func() { setContainerPaused(t, container, true) },
		retireCodesFailedMessage:   func() { setContainerPaused(t, container, false) },
	})

	_, err := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit)
	hooks.requireFinished(t)
	requireRecordedFailureCauses(t, index, err)
	requireFailedRowCodes(t, waitForAttemptRows(t, stack, written, 1)[0], uncertainRecordingCode)
}
