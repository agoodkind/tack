package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"goodkind.io/tack/internal/audit"
)

// TestRuntimeAuditReadRecoveryWorker admits one renewed read and one abandoned
// read on each store. audit.KeepReadOwned renews the first read, and the test
// waits past the lease in real time. The test then starts audit.RunReadRecovery,
// the loop that each application instance runs. The loop must save an
// incomplete event for the abandoned read and must leave the renewed read
// pending. The renewal must stop without an error, the renewed read must
// finalize as completed, and the abandoned read must refuse finalization.
func TestRuntimeAuditReadRecoveryWorker(t *testing.T) {
	backends := readBackends(t)
	renewed := make([]readFixture, len(backends))
	abandoned := make([]readFixture, len(backends))
	stops := make([]func() error, len(backends))
	for position, backend := range backends {
		renewed[position], abandoned[position] = newReadFixture(t), newReadFixture(t)
		renewed[position].begin(t, backend.store)
		abandoned[position].begin(t, backend.store)
		stops[position] = audit.KeepReadOwned(t.Context(), backend.store, renewed[position].completion.Token)
	}
	waitForReadLeases(t)

	for position, backend := range backends {
		startReadRecovery(t, backend.store)
		abandonedID := abandoned[position].admission.Token.EventID
		waitFor(t, backend.name+" recovery of read "+abandonedID.String(), func() bool {
			return len(backend.terminal(t, abandonedID)) > 0
		})
		requireTerminal(t, backend, abandonedID, audit.OutcomeIncomplete)
		if err := stops[position](); err != nil {
			t.Fatalf("%s renewal of read %s stopped with %v, want nil", backend.name, renewed[position].admission.Token.EventID, err)
		}
		if err := backend.store.FinalizeRead(t.Context(), renewed[position].completion); err != nil {
			t.Fatalf("%s finalization of the renewed read: %v", backend.name, err)
		}
		requireTerminal(t, backend, renewed[position].admission.Token.EventID, audit.OutcomeCompleted)
		if err := backend.store.FinalizeRead(t.Context(), abandoned[position].completion); !errors.Is(err, audit.ErrReadOwnerLost) {
			t.Fatalf("%s finalization of the recovered read = %v, want audit.ErrReadOwnerLost", backend.name, err)
		}
	}
}

// startReadRecovery runs audit.RunReadRecovery on store until the test ends.
func startReadRecovery(t *testing.T, store audit.ReadLifecycle) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var loop sync.WaitGroup
	loop.Go(func() { audit.RunReadRecovery(ctx, store) })
	t.Cleanup(func() {
		cancel()
		loop.Wait()
	})
}
