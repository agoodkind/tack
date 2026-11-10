//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// cleanupNotStarted is the failure code of a close or delete step that returned
// without a transaction attempt.
const cleanupNotStarted = searchdomain.CleanupStep("session_cleanup_not_started")

// TestSearchSessionAuditCleanupCodeAfterConflictRetry checks that the close
// event records session_close_failed when the context ends during an attempt
// that encounters conflict error 1020.
func TestSearchSessionAuditCleanupCodeAfterConflictRetry(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	ctx := integration.WithTestOperation(t, t.Context())
	session := storedSearchSession(sessionTokens(sessionStoreTokenBytes * 16))
	if _, err := store.Create(ctx, session); err != nil {
		t.Fatalf("create session %s: %v", session.ID, err)
	}
	mark := searchOutboxTail(t, stores)
	closing, cancel := context.WithCancel(ctx)
	defer cancel()
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCommitReady: func() {
			integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), []byte(invalidHeaderValue))
			cancel()
		},
	})

	closeErr := store.BeginCleanup(closing, session.ID)
	hooks.requireFinished(t)
	if !errors.Is(closeErr, context.Canceled) || errors.Is(closeErr, searchdomain.ErrSessionStepNotStarted) {
		t.Fatalf("BeginCleanup after a conflict and an ended context returned %v, want %v without %v", closeErr, context.Canceled, searchdomain.ErrSessionStepNotStarted)
	}
	if err := store.RecordCleanupFailure(closing, session.ID, searchdomain.CleanupClose, closeErr); err != nil {
		t.Fatalf("record the close step that made an attempt: %v", err)
	}
	requireCleanupCode(t, stores, mark, session, searchdomain.CleanupClose, cleanupNotStarted)
}

func requireCleanupCode(t *testing.T, stores *fdbadapter.Stores, mark []byte, session searchdomain.Session, wanted, unwanted searchdomain.CleanupStep) {
	t.Helper()
	events := cleanupFailureEvents(t, stores, mark, session.ID, wanted)
	if len(events) != 1 || events[0].Outcome != audit.OutcomeFailed || events[0].Verb != string(audit.VerbSearchSessionUpdate) {
		t.Fatalf("session %s has %s events %+v, want one failed %s event", session.ID, wanted, events, audit.VerbSearchSessionUpdate)
	}
	if other := cleanupFailureEvents(t, stores, mark, session.ID, unwanted); len(other) != 0 {
		t.Fatalf("session %s has %d %s events, want 0", session.ID, len(other), unwanted)
	}
}

// TestSearchSessionAuditCleanupCodesSeparateNotStartedSteps checks the failure
// code of a close step that returned without an attempt and of a close step
// that began and timed out during a FoundationDB pause.
func TestSearchSessionAuditCleanupCodesSeparateNotStartedSteps(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	ctx := integration.WithTestOperation(t, t.Context())
	tokens := sessionTokens(sessionStoreTokenBytes * 16)
	unstarted, begun := storedSearchSession(tokens), storedSearchSession(tokens)
	for _, session := range []searchdomain.Session{unstarted, begun} {
		if _, err := store.Create(ctx, session); err != nil {
			t.Fatalf("create session %s: %v", session.ID, err)
		}
	}
	mark := searchOutboxTail(t, stores)

	ended, cancel := context.WithDeadline(ctx, clock.Now())
	defer cancel()
	notStartedErr := store.BeginCleanup(ended, unstarted.ID)
	if !errors.Is(notStartedErr, context.DeadlineExceeded) {
		t.Fatalf("BeginCleanup after the deadline returned %v, want %v", notStartedErr, context.DeadlineExceeded)
	}
	if err := store.RecordCleanupFailure(ended, unstarted.ID, searchdomain.CleanupClose, notStartedErr); err != nil {
		t.Fatalf("record the close step that did not start: %v", err)
	}

	setContainerPaused(t, container, true)
	timedOutErr := store.BeginCleanup(ctx, begun.ID)
	setContainerPaused(t, container, false)
	if !errors.Is(timedOutErr, fdb.Error{Code: transactionTimedOutNumber}) {
		t.Fatalf("BeginCleanup during the pause returned %v, want FoundationDB error %d", timedOutErr, transactionTimedOutNumber)
	}
	if err := store.RecordCleanupFailure(ctx, begun.ID, searchdomain.CleanupClose, timedOutErr); err != nil {
		t.Fatalf("record the close step that timed out: %v", err)
	}

	requireCleanupCode(t, stores, mark, unstarted, cleanupNotStarted, searchdomain.CleanupClose)
	requireCleanupCode(t, stores, mark, begun, searchdomain.CleanupClose, cleanupNotStarted)
}
