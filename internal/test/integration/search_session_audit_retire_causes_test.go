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

const transactionTimedOutNumber = 1031

// TestSearchSessionAuditRetireLostRecordingKeepsCauses checks that
// RetireIndexSessions reports remaining work and returns an error matching
// audit.ErrEventNotRecorded, context.Canceled, and the FoundationDB timeout
// when the failure event write fails. The test also checks that the outbox
// does not contain a failed close event for the session.
func TestSearchSessionAuditRetireLostRecordingKeepsCauses(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-lost-recording"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	retireContext, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	defer cancel()
	gate := installRetirePauseGate(t, container, session.ID.String(), retireAttemptSavedMessage)
	pause := gate.pending[retireAttemptSavedMessage]
	gate.pending[retireAttemptSavedMessage] = func() {
		pause()
		cancel()
	}

	remaining, err := rebuilds.RetireIndexSessions(retireContext, index, retireSliceLimit)
	gate.requireFinished(t)
	wanted := []error{audit.ErrEventNotRecorded, context.Canceled, fdb.Error{Code: transactionTimedOutNumber}}
	for _, want := range wanted {
		if !remaining || !errors.Is(err, want) {
			t.Fatalf("RetireIndexSessions of %s with a lost failure event = %t, %v, want remaining work and errors matching %v", index, remaining, err, wanted)
		}
	}
	if events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose); len(events) != 0 {
		t.Fatalf("outbox contains %d failed close events for session %s", len(events), session.ID)
	}
}
