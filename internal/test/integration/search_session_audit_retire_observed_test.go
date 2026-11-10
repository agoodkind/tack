//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

type storedAttemptFields struct {
	Deadline   time.Time       `json:"deadline"`
	ObservedAt time.Time       `json:"observed_at,omitzero"`
	Event      json.RawMessage `json:"event"`
	Cleanup    string          `json:"cleanup_failure,omitempty"`
	Recording  string          `json:"recording_failure,omitempty"`
}

// TestSearchSessionAuditRetireFailedWriteClearsObservedRecord runs a
// second RetireIndexSessions call from a log hook at the cleanup failure
// log. Recovery in the second call stores ObservedAt in the first
// attempt record. The first call then saves its failed event. The test
// requires one failed event for the first operation and requires zero
// attempt records. This test does not cover a concurrent race with
// FoundationDB error 1020. Code inspection establishes that result.
func TestSearchSessionAuditRetireFailedWriteClearsObservedRecord(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-observed-write"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	ownerContext := withFutureOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(ownerContext)
	if err != nil {
		t.Fatalf("read the owner operation: %v", err)
	}
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			retireCleanupFailedMessage: func() { observeAttemptWithFailingCall(t, stores, index) },
		},
	)

	_, err = rebuilds.RetireIndexSessions(ownerContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	requireChangeEventFailure(t, "RetireIndexSessions of "+index, err)
	if errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf(
			"RetireIndexSessions of %s with a saved failed event = %v, want no %v",
			index,
			err,
			audit.ErrEventNotRecorded,
		)
	}
	attemptRecordValues(t, 0)
	found := 0
	for _, event := range cleanupFailureEvents(
		t,
		stores,
		mark,
		session.ID,
		searchdomain.CleanupClose,
	) {
		if event.Context.OperationID == operation.Context.OperationID {
			found++
		}
	}
	if found != 1 {
		t.Fatalf(
			"operation %s has %d failed close events, want 1",
			operation.Context.OperationID,
			found,
		)
	}
}

// TestSearchSessionAuditRetireCodeSaveKeepsObservation runs a second
// RetireIndexSessions call at the cleanup failure log. The second call
// stores ObservedAt. The test pauses FoundationDB, and the failure event
// write fails. The test resumes FoundationDB at the audit failure log.
// The failure code save then succeeds. The test requires the record to
// retain the same ObservedAt and contain both failure codes. This test
// does not cover a concurrent race with FoundationDB error 1020.
// Code inspection establishes that result.
func TestSearchSessionAuditRetireCodeSaveKeepsObservation(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-observed-codes"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	var observed time.Time
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			retireCleanupFailedMessage: func() {
				observed = observeAttemptWithFailingCall(t, stores, index)
				setContainerPaused(t, container, true)
			},
			retireAuditFailedMessage: func() { setContainerPaused(t, container, false) },
		},
	)

	_, err := rebuilds.RetireIndexSessions(
		withFutureOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	hooks.requireFinished(t)
	if !errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf(
			"RetireIndexSessions of %s with saved codes = %v, want %v",
			index,
			err,
			audit.ErrEventNotRecorded,
		)
	}
	stored := readStoredAttempt(t)
	if !stored.ObservedAt.Equal(observed) || stored.Cleanup != unclassifiedFailureCode ||
		stored.Recording != transactionTimedOutCode {
		t.Fatalf(
			"stored attempt has observation %s and codes %q and %q, want observation %s and codes %q and %q",
			stored.ObservedAt,
			stored.Cleanup,
			stored.Recording,
			observed,
			unclassifiedFailureCode,
			transactionTimedOutCode,
		)
	}
}

// TestSearchSessionAuditRetireClearRemovesObservedRecord runs a second
// RetireIndexSessions call from a log hook after the delete step commits.
// Recovery in the second call stores ObservedAt in the attempt record.
// The first call then clears the record. The test requires the first
// call to finish retirement without an error and leave zero attempt
// records. This test does not cover a concurrent race with FoundationDB
// error 1020. Code inspection establishes that result.
func TestSearchSessionAuditRetireClearRemovesObservedRecord(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-observed-clear"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			sessionDeletedMessage: func() {
				remaining, err := rebuilds.RetireIndexSessions(
					integration.WithTestOperation(t, t.Context()),
					index,
					retireSliceLimit,
				)
				if err != nil || !remaining {
					t.Fatalf(
						"RetireIndexSessions of %s with an active attempt = %t, %v, want remaining work",
						index,
						remaining,
						err,
					)
				}
				if readStoredAttempt(t).ObservedAt.IsZero() {
					t.Fatalf("the attempt record of index %s has no observation time", index)
				}
			},
		},
	)

	remaining, err := rebuilds.RetireIndexSessions(
		integration.WithTestOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	hooks.requireFinished(t)
	if err != nil || remaining {
		t.Fatalf(
			"RetireIndexSessions of %s with an observed record = %t, %v, want a finished retirement",
			index,
			remaining,
			err,
		)
	}
	attemptRecordValues(t, 0)
}

// observeAttemptWithFailingCall runs a second RetireIndexSessions call
// under an operation with an invalid start time. Recovery in the second
// call stores ObservedAt in the attempt record. The second call then
// fails its own close step and clears its own attempt. The function
// returns the stored observation time.
func observeAttemptWithFailingCall(
	t *testing.T,
	stores *fdbadapter.Stores,
	index string,
) time.Time {
	t.Helper()
	_, err := stores.SearchRebuilds(clock.Wall{}).RetireIndexSessions(
		withFutureOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	requireChangeEventFailure(t, "RetireIndexSessions recovery of "+index, err)
	observed := readStoredAttempt(t).ObservedAt
	if observed.IsZero() {
		t.Fatalf("the attempt record of index %s has no observation time", index)
	}
	return observed
}

func readStoredAttempt(t *testing.T) storedAttemptFields {
	t.Helper()
	var stored storedAttemptFields
	if err := json.Unmarshal(retireFamilyValue(t, retireAttemptFamily), &stored); err != nil {
		t.Fatalf("decode the attempt record: %v", err)
	}
	return stored
}
