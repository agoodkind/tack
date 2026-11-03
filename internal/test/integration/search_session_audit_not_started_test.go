//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// cleanupNotStarted is the failure code of a close or delete step that returned
// without a transaction attempt.
const cleanupNotStarted = searchdomain.CleanupStep("session_cleanup_not_started")

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

	cases := []struct {
		session  searchdomain.Session
		wanted   searchdomain.CleanupStep
		unwanted searchdomain.CleanupStep
	}{
		{session: unstarted, wanted: cleanupNotStarted, unwanted: searchdomain.CleanupClose},
		{session: begun, wanted: searchdomain.CleanupClose, unwanted: cleanupNotStarted},
	}
	for _, current := range cases {
		events := cleanupFailureEvents(t, stores, mark, current.session.ID, current.wanted)
		if len(events) != 1 || events[0].Outcome != audit.OutcomeFailed || events[0].Verb != string(audit.VerbSearchSessionUpdate) {
			t.Fatalf("session %s has %s events %+v, want one failed %s event", current.session.ID, current.wanted, events, audit.VerbSearchSessionUpdate)
		}
		if other := cleanupFailureEvents(t, stores, mark, current.session.ID, current.unwanted); len(other) != 0 {
			t.Fatalf("session %s has %d %s events, want 0", current.session.ID, len(other), current.unwanted)
		}
	}
}
