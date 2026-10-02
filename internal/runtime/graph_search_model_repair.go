package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// modelRepairLoop is the loop that deploys a stuck serving model again.
type modelRepairLoop struct {
	cancel  context.CancelFunc
	loop    sync.WaitGroup
	started bool
}

// StartSearchModelRepair starts the loop that checks the serving model
// deployment once per repair interval. The loop stops when ctx ends or
// CloseContext runs. A process without a search endpoint starts no loop.
// With OPENSEARCH_MODEL_REPAIR_ENABLED false, the default, it starts no loop
// and logs search.model_repair.disabled.
func (g *Graph) StartSearchModelRepair(ctx context.Context) {
	if g.search.repair == nil || g.modelRepair.started {
		return
	}
	if !g.search.repair.Enabled() {
		telemetry.L(ctx).InfoContext(ctx, "search.model_repair.disabled")
		return
	}
	g.modelRepair.started = true
	loopContext, cancel := context.WithCancel(ctx)
	g.modelRepair.cancel = cancel
	repair := g.search.repair
	g.modelRepair.loop.Go(func() {
		defer recoverSearchWorker(loopContext, "search.model_repair.loop_panicked")
		runModelRepairLoop(loopContext, repair)
	})
	settings := repair.Settings()
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.started", slog.Duration("interval", settings.CheckInterval),
		slog.Duration("stuck_after", settings.StuckAfter), slog.Duration("attempt_spacing", settings.AttemptSpacing),
		slog.Int("max_attempts", settings.MaxAttempts))
}

// runModelRepairLoop runs one check per interval until ctx ends. The next
// interval starts after the current check returns.
func runModelRepairLoop(ctx context.Context, repair *service.SearchModelRepair) {
	timer := time.NewTimer(repair.Settings().CheckInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			checkModelRepair(ctx, repair)
			timer.Reset(repair.Settings().CheckInterval)
		}
	}
}

// checkModelRepair runs one check and recovers a panic in that check. It
// drops the returned error. Check logs each failure at Error.
func checkModelRepair(ctx context.Context, repair *service.SearchModelRepair) {
	defer recoverSearchWorker(ctx, "search.model_repair.check_panicked")
	_ = repair.Check(ctx)
}

// stopSearchModelRepair stops the repair loop and waits for it.
func (g *Graph) stopSearchModelRepair() {
	if g.modelRepair.cancel != nil {
		g.modelRepair.cancel()
	}
	if g.modelRepair.started {
		g.modelRepair.loop.Wait()
	}
}
