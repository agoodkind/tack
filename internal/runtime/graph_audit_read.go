package runtime

import (
	"context"
	"log/slog"
	"sync"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// auditReadRecovery runs one recovery loop per read lifecycle store.
type auditReadRecovery struct {
	stores  []audit.ReadLifecycle
	cancel  context.CancelFunc
	loop    sync.WaitGroup
	started bool
}

// newAuditReadRecovery returns the recovery loops for the product and
// operator read stores.
func newAuditReadRecovery(stores ...audit.ReadLifecycle) *auditReadRecovery {
	return &auditReadRecovery{stores: stores, cancel: nil, loop: sync.WaitGroup{}, started: false}
}

// StartAuditReadRecovery starts the loops that recover expired pending reads
// and delete old published receipts. Every application instance runs them
// without a shared lock. The loops stop when ctx ends or CloseContext runs.
func (g *Graph) StartAuditReadRecovery(ctx context.Context) {
	recovery := g.auditReads
	if recovery.started {
		return
	}
	recovery.started = true
	loopContext, cancel := context.WithCancel(ctx)
	recovery.cancel = cancel
	for _, store := range recovery.stores {
		current := store
		recovery.loop.Go(func() {
			defer recoverSearchWorker(loopContext, "audit.read_recovery.loop_panicked")
			audit.RunReadRecovery(loopContext, current)
		})
	}
	telemetry.L(ctx).InfoContext(ctx, "audit.read_recovery.started", slog.Int("stores", len(recovery.stores)))
}

// stopAuditReadRecovery stops the recovery loops and waits for them.
func (g *Graph) stopAuditReadRecovery() {
	recovery := g.auditReads
	if recovery.cancel != nil {
		recovery.cancel()
	}
	if recovery.started {
		recovery.loop.Wait()
	}
}
