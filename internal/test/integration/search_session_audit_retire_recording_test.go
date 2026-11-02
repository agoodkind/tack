//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
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
	cleanupRecordingFailure  = "record the failed cleanup step"
	transactionTimedOutText  = "FoundationDB error code 1031"
	transactionTimedOutCode  = "foundationdb_1031"
	unclassifiedFailureCode  = "unclassified"
	retireResultNotSaved     = "cleanup_result_not_saved"
	retireCodesFailedMessage = "search.session.retire_codes_failed"
	// retireAttemptWait exceeds the 100 second retirement attempt lease.
	retireAttemptWait = 101 * time.Second
)

type retireFailureEvidence struct {
	Cleanup   string `json:"cleanup_failure_code"`
	Recording string `json:"recording_failure_code"`
}

// TestSearchSessionAuditRetireRecoveryOmitsUnsavedCodes pauses FoundationDB
// before the failure event write and resumes it after the failure code save
// fails. After the attempt lease expires, the test checks that
// RetireIndexSessions saves one incomplete event with the original operation
// ID, the cleanup_result_not_saved code, and zero failure codes in its
// evidence. The causes remain unknown because neither write persisted.
func TestSearchSessionAuditRetireRecoveryOmitsUnsavedCodes(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-unsaved-codes"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	lostContext := withFutureOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(lostContext)
	if err != nil {
		t.Fatalf("read the lost attempt operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() { setContainerPaused(t, container, true) },
		retireCodesFailedMessage:   func() { setContainerPaused(t, container, false) },
	})

	_, err = rebuilds.RetireIndexSessions(lostContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if !errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s without saved evidence = %v, want %v", index, err, audit.ErrEventNotRecorded)
	}
	waitUntilTime(t, clock.Now().Add(retireAttemptWait))
	if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err != nil {
		t.Fatalf("RetireIndexSessions of %s after the attempt lease: %v", index, err)
	}
	events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved))
	if len(events) != 1 || events[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf("session %s recovered events = %+v, want 1 under operation %s", session.ID, events, operation.Context.OperationID)
	}
	var evidence retireFailureEvidence
	if len(events[0].Extra) != 0 {
		if err := json.Unmarshal(events[0].Extra, &evidence); err != nil {
			t.Fatalf("decode the recovered evidence %s: %v", events[0].Extra, err)
		}
	}
	if evidence.Cleanup != "" || evidence.Recording != "" {
		t.Fatalf("recovered evidence = %s, want zero failure codes", events[0].Extra)
	}
}

// TestSearchSessionAuditRetireRecoversLostFailureEvent tests the close and
// delete steps. The first RetireIndexSessions call uses an operation that
// fails event validation, and the test pauses FoundationDB before the failure
// event write. The call must return the cleanup and recording errors and keep
// the session. The second call uses a valid operation during the attempt lease.
// It must delete the session, report remaining work, and save zero recovered
// events. The third call runs after the lease. It must finish retirement and
// save one incomplete event with the actor, operation ID, start time,
// organization, and owner of the first attempt.
func TestSearchSessionAuditRetireRecoversLostFailureEvent(t *testing.T) {
	cases := []struct {
		index   string
		closing bool
		verb    audit.Verb
	}{
		{index: "node-pages-close-lost", closing: false, verb: audit.VerbSearchSessionUpdate},
		{index: "node-pages-delete-lost", closing: true, verb: audit.VerbSearchSessionDelete},
	}
	for _, current := range cases {
		t.Run(current.index, func(t *testing.T) {
			stores := sessionStoreStores(t)
			rebuilds := stores.SearchRebuilds(clock.Wall{})
			store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
			container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
			beginRetiringRebuild(t, stores, current.index)
			session := retiringSession(t, stores, current.index)
			if current.closing {
				if err := store.BeginCleanup(integration.WithTestOperation(t, t.Context()), session.ID); err != nil {
					t.Fatalf("close session %s: %v", session.ID, err)
				}
			}
			mark := searchOutboxTail(t, stores)
			gate := installRetirePauseGate(t, container, session.ID.String(), retireCleanupFailedMessage)
			lostContext := withFutureOperation(t, t.Context())
			operation, err := audit.RequireMutationContext(lostContext)
			if err != nil {
				t.Fatalf("read the lost attempt operation: %v", err)
			}

			remaining, err := rebuilds.RetireIndexSessions(lostContext, current.index, retireSliceLimit)
			gate.requireFinished(t)
			if err == nil || !remaining || !errors.Is(err, audit.ErrEventNotRecorded) {
				t.Fatalf("RetireIndexSessions of %s without a saved failure event = %t, %v, want a remaining session and %v", current.index, remaining, err, audit.ErrEventNotRecorded)
			}
			for _, want := range []string{changeEventFailure, cleanupRecordingFailure, transactionTimedOutText} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("RetireIndexSessions of %s error = %v, want text %q", current.index, err, want)
				}
			}
			stored, err := store.Load(t.Context(), session.ID)
			if err != nil || stored.Closing != current.closing {
				t.Fatalf("session %s after the lost failure event = %+v, %v, want closing %t", session.ID, stored, err, current.closing)
			}

			remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), current.index, retireSliceLimit)
			if err != nil || !remaining {
				t.Fatalf("RetireIndexSessions of %s during the attempt lease = %t, %v, want remaining work", current.index, remaining, err)
			}
			if _, err := store.Load(t.Context(), session.ID); !errors.Is(err, searchdomain.ErrSessionNotFound) {
				t.Fatalf("load session %s after the successful retry = %v, want %v", session.ID, err, searchdomain.ErrSessionNotFound)
			}
			if events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved)); len(events) != 0 {
				t.Fatalf("session %s has %d recovered events during the attempt lease, want 0", session.ID, len(events))
			}

			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), current.index, retireSliceLimit)
			if err != nil || remaining {
				t.Fatalf("RetireIndexSessions of %s after the attempt lease = %t, %v, want a finished retirement", current.index, remaining, err)
			}
			events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved))
			if len(events) != 1 {
				t.Fatalf("session %s has %d recovered attempt events, want 1", session.ID, len(events))
			}
			event := events[0]
			if event.Actor.ID != integration.TestActorID || event.Context.OperationID != operation.Context.OperationID ||
				!event.Context.StartedAt.Equal(operation.Context.StartedAt) || event.Verb != string(current.verb) ||
				event.Outcome != audit.OutcomeIncomplete || event.Context.OrgID != session.AuthorityID ||
				event.Context.Target == nil || event.Context.Target.OwnerID != session.PrincipalID {
				t.Fatalf("recovered attempt event = %+v, target %+v, want %s by actor %s under operation %s started at %s",
					event, event.Context.Target, current.verb, integration.TestActorID, operation.Context.OperationID, operation.Context.StartedAt)
			}
			var evidence retireFailureEvidence
			if err := json.Unmarshal(event.Extra, &evidence); err != nil || evidence.Cleanup != unclassifiedFailureCode || evidence.Recording != transactionTimedOutCode {
				t.Fatalf("recovered attempt evidence = %s, %v, want cleanup code %q and recording code %q", event.Extra, err, unclassifiedFailureCode, transactionTimedOutCode)
			}
		})
	}
}
