//go:build runtimeaudit

package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireCommitSettle      = 6 * time.Second
	retireResultUncertain   = "cleanup_result_uncertain"
	retireCommitReady       = "search.session.close_commit_ready"
	retireStorageFailed     = "search.session.storage_failed"
	retireRebuildEnded      = "the index replacement of this retirement attempt has ended"
	invalidHeaderValue      = "{"
	retireLimitedSliceLimit = 2
	retireLimitedRecords    = 3
)

// TestSearchSessionAuditRetireCommitTimeoutIsUncertain pauses FoundationDB
// after the close transaction prepares its writes. The commit times out with
// error 1031, and the test resumes FoundationDB when cleanup logs the failure.
// The test requires one incomplete event with cleanup_result_uncertain and the
// owner operation ID. It requires zero failed close events. FoundationDB can
// apply the commit after it resumes, and the test cannot choose that outcome.
// After a wait, the test reads and logs the session state. It requires one
// update change event if the session is closing and zero if it is open.
func TestSearchSessionAuditRetireCommitTimeoutIsUncertain(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-uncertain"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	ownerContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(ownerContext)
	if err != nil {
		t.Fatalf("read the owner operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCommitReady:          func() { setContainerPaused(t, container, true) },
		retireCleanupFailedMessage: func() { setContainerPaused(t, container, false) },
	})

	remaining, err := rebuilds.RetireIndexSessions(ownerContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if err == nil || !remaining || !strings.Contains(err.Error(), transactionTimedOutText) {
		t.Fatalf("RetireIndexSessions of %s with a timed-out commit = %t, %v, want remaining work and text %q", index, remaining, err, transactionTimedOutText)
	}
	events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultUncertain))
	if len(events) != 1 || events[0].Outcome != audit.OutcomeIncomplete || events[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf("session %s uncertain events = %+v, want 1 incomplete event under operation %s", session.ID, events, operation.Context.OperationID)
	}
	if failed := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose); len(failed) != 0 {
		t.Fatalf("session %s has %d failed close events after an uncertain commit, want 0", session.ID, len(failed))
	}
	waitUntilTime(t, clock.Now().Add(retireCommitSettle))
	stored, err := stores.SearchSessions(clock.Wall{}, sessionStoreIdle).Load(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("load session %s after the uncertain commit: %v", session.ID, err)
	}
	wantChanges := 0
	if stored.Closing {
		wantChanges = 1
	}
	t.Logf("the timed-out commit applied: %t", stored.Closing)
	if count := sessionChangeEvents(t, stores, mark, session, operation.Context.OperationID, audit.VerbSearchSessionUpdate); count != wantChanges {
		t.Fatalf("session %s closing %t has %d update change events, want %d", session.ID, stored.Closing, count, wantChanges)
	}
}

// TestSearchSessionAuditRetireCommitConflictIsDefinite replaces the session
// header through a log hook after the close transaction reads it. The commit
// fails with FoundationDB conflict error 1020, and the retry finds an invalid
// header. The test requires one failed close event and zero uncertain events.
func TestSearchSessionAuditRetireCommitConflictIsDefinite(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-conflict"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCommitReady: func() {
			integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), []byte(invalidHeaderValue))
		},
	})

	if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err == nil {
		t.Fatalf("RetireIndexSessions of %s after a conflict and an invalid header returned no error", index)
	}
	hooks.requireFinished(t)
	failed := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose)
	if len(failed) != 1 || failed[0].Outcome != audit.OutcomeFailed {
		t.Fatalf("session %s failed close events = %+v, want 1 failed event", session.ID, failed)
	}
	if uncertain := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultUncertain)); len(uncertain) != 0 {
		t.Fatalf("session %s has %d uncertain events after a definite conflict, want 0", session.ID, len(uncertain))
	}
}

// TestSearchSessionAuditRetireFailedClearIsReported pauses FoundationDB
// after the delete step commits. The clear fails. The test requires an
// error without audit.ErrEventNotRecorded and a report of remaining work.
// A later call during the lease must also report remaining work.
func TestSearchSessionAuditRetireFailedClearIsReported(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-failed-clear"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireSessionDeletedMessage: func() { setContainerPaused(t, container, true) },
		retireStorageFailed:         func() { setContainerPaused(t, container, false) },
	})

	remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
	hooks.requireFinished(t)
	if !remaining || err == nil || errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with a failed clear = %t, %v, want remaining work and an error without %v", index, remaining, err, audit.ErrEventNotRecorded)
	}
	remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
	if err != nil || !remaining {
		t.Fatalf("RetireIndexSessions of %s during the attempt lease = %t, %v, want remaining work", index, remaining, err)
	}
}

// TestSearchSessionAuditRetireRefusesEndedRebuild checks that
// RetireIndexSessions refuses the attempt save when the replacement record is
// absent and leaves the session open.
func TestSearchSessionAuditRetireRefusesEndedRebuild(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	index := "node-pages-ended"
	session := retiringSession(t, stores, index)
	_, err := stores.SearchRebuilds(clock.Wall{}).RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
	if err == nil || !strings.Contains(err.Error(), retireRebuildEnded) {
		t.Fatalf("RetireIndexSessions of %s without a replacement = %v, want text %q", index, err, retireRebuildEnded)
	}
	stored, err := store.Load(t.Context(), session.ID)
	if err != nil || stored.Closing {
		t.Fatalf("session %s after the refused attempt = %+v, %v, want an open session", session.ID, stored, err)
	}
}

// TestSearchSessionAuditRetireRecoveryHonorsLimit stores three invalid records
// and sets the limit to two. The first pass must recover two records and
// report remaining work. The second pass must recover the third and finish.
func TestSearchSessionAuditRetireRecoveryHonorsLimit(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-limit"
	beginRetiringRebuild(t, stores, index)
	mark := searchOutboxTail(t, stores)
	for range retireLimitedRecords {
		integration.ReplaceRawValue(t, retireAttemptKey(t, index), []byte(invalidAttemptValue))
	}
	remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireLimitedSliceLimit)
	if err != nil || !remaining {
		t.Fatalf("RetireIndexSessions of %s above its limit = %t, %v, want remaining work", index, remaining, err)
	}
	if events := invalidAttemptEvents(t, stores, mark); len(events) != retireLimitedSliceLimit {
		t.Fatalf("index %s has %d invalid attempt events after one pass, want %d", index, len(events), retireLimitedSliceLimit)
	}
	remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireLimitedSliceLimit)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s below its limit = %t, %v, want a finished retirement", index, remaining, err)
	}
	if events := invalidAttemptEvents(t, stores, mark); len(events) != retireLimitedRecords {
		t.Fatalf("index %s has %d invalid attempt events after two passes, want %d", index, len(events), retireLimitedRecords)
	}
}
