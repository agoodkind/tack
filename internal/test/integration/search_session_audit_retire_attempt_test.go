//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const retireAttemptFenced = "recovery already resolved this retirement attempt"

// TestSearchSessionAuditRetireLateOwnerSavesFailure pauses the first
// RetireIndexSessions call at its cleanup failure log until the attempt lease
// passes. A second call recovers the attempt and deletes the session before the
// first call continues. The test checks that the first call returns only the
// cleanup error and saves the failed event. It checks that the session has one
// failed event and one incomplete event with different event IDs and the
// operation ID of the first call.
func TestSearchSessionAuditRetireLateOwnerSavesFailure(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-late-owner"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	lateContext := withFutureOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(lateContext)
	if err != nil {
		t.Fatalf("read the late attempt operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCleanupFailedMessage: func() {
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err != nil {
				t.Fatalf("recover the late attempt of index %s: %v", index, err)
			}
		},
	})

	_, err = rebuilds.RetireIndexSessions(lateContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	requireChangeEventFailure(t, "RetireIndexSessions of "+index, err)
	if strings.Contains(err.Error(), cleanupRecordingFailure) {
		t.Fatalf("RetireIndexSessions of %s error = %v, want a saved failure event", index, err)
	}
	failed := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose)
	recovered := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved))
	if len(failed) != 1 || len(recovered) != 1 {
		t.Fatalf("session %s has %d failed and %d incomplete events, want 1 of each", session.ID, len(failed), len(recovered))
	}
	if failed[0].EventID == recovered[0].EventID || failed[0].Outcome != audit.OutcomeFailed || recovered[0].Outcome != audit.OutcomeIncomplete ||
		failed[0].Context.OperationID != operation.Context.OperationID || recovered[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf("failed event = %+v, incomplete event = %+v, want separate event IDs under operation %s", failed[0], recovered[0], operation.Context.OperationID)
	}
}

// TestSearchSessionAuditRetireLateEffectIsFenced pauses the first
// RetireIndexSessions call with a log hook after it saves its close attempt,
// until the attempt lease expires. A second call recovers that attempt, then
// its own close step fails event validation. The first call then runs cleanup
// and must return errRetireAttemptFenced and report remaining work. The first
// operation must have one incomplete event, zero update change events, and
// zero failed events. The session must stay open. A third call with a valid
// operation must finish retirement.
func TestSearchSessionAuditRetireLateEffectIsFenced(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	index := "node-pages-late-effect"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	lateContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(lateContext)
	if err != nil {
		t.Fatalf("read the late effect operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireAttemptSavedMessage: func() {
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			_, recoverErr := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit)
			requireChangeEventFailure(t, "RetireIndexSessions recovery of "+index, recoverErr)
		},
	})

	remaining, err := rebuilds.RetireIndexSessions(lateContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if err == nil || !remaining || !strings.Contains(err.Error(), retireAttemptFenced) || errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with a late effect = %t, %v, want remaining work and only the fence text %q", index, remaining, err, retireAttemptFenced)
	}
	recovered := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved))
	if len(recovered) != 1 || recovered[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf("session %s recovered events = %+v, want 1 under operation %s", session.ID, recovered, operation.Context.OperationID)
	}
	if count := sessionChangeEvents(t, stores, mark, session, operation.Context.OperationID, audit.VerbSearchSessionUpdate); count != 0 {
		t.Fatalf("session %s has %d update change events under the fenced operation %s, want 0", session.ID, count, operation.Context.OperationID)
	}
	for _, failed := range cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose) {
		if failed.Context.OperationID == operation.Context.OperationID {
			t.Fatalf("the fenced operation %s wrote the failed event %+v", operation.Context.OperationID, failed)
		}
	}
	stored, err := store.Load(t.Context(), session.ID)
	if err != nil || stored.Closing {
		t.Fatalf("session %s after the fenced effect = %+v, %v, want an open session", session.ID, stored, err)
	}
	remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s after the fenced effect = %t, %v, want a finished retirement", index, remaining, err)
	}
}

// TestSearchSessionAuditRetireClearReportsLostOwnership checks recovery after
// a log hook pauses RetireIndexSessions after its delete step commits and
// before it clears the attempt record. The hook waits until the attempt lease
// expires. A second call recovers the attempt. The test checks that the first
// call returns audit.ErrEventNotRecorded and reports remaining work. It also
// checks that the first operation has one delete change event and one
// incomplete event, and that a third call finishes retirement.
func TestSearchSessionAuditRetireClearReportsLostOwnership(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-lost-clear"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	ownerContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(ownerContext)
	if err != nil {
		t.Fatalf("read the owner operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireSessionDeletedMessage: func() {
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err != nil {
				t.Fatalf("recover the attempt of index %s: %v", index, err)
			}
		},
	})

	remaining, err := rebuilds.RetireIndexSessions(ownerContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if err == nil || !remaining || !strings.Contains(err.Error(), retireAttemptFenced) || errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with a lost clear = %t, %v, want remaining work and only the fence text %q", index, remaining, err, retireAttemptFenced)
	}
	recovered := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultNotSaved))
	if len(recovered) != 1 || recovered[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf("session %s recovered events = %+v, want 1 under operation %s", session.ID, recovered, operation.Context.OperationID)
	}
	if count := sessionChangeEvents(t, stores, mark, session, operation.Context.OperationID, audit.VerbSearchSessionDelete); count != 1 {
		t.Fatalf("session %s has %d delete change events under operation %s, want 1", session.ID, count, operation.Context.OperationID)
	}
	remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s after the lost clear = %t, %v, want a finished retirement", index, remaining, err)
	}
}

func sessionChangeEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte, session searchdomain.Session, operationID uuid.UUID, verb audit.Verb) int {
	t.Helper()
	entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, searchAuditPageLimit)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	count := 0
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode outbox entry %s: %v", entry.Event, err)
		}
		target := event.Context.Target
		if event.Verb == string(verb) && event.Context.Phase == audit.PhaseChange && event.Context.OperationID == operationID &&
			target != nil && target.NodeType == audit.TargetSearchSession && strings.Contains(target.URI, session.ID.String()) {
			count++
		}
	}
	return count
}
