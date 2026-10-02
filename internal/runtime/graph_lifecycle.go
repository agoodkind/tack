package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/telemetry"
)

// PingYugabyte verifies that this instance can open a Yugabyte connection now.
// It dials outside the pool because the pool retains a slot while the driver
// cleans up a dead connection (see postgres.PingFreshConnection).
func (g *Graph) PingYugabyte(ctx context.Context) error {
	if err := postgres.PingFreshConnection(ctx, g.pool); err != nil {
		wrapped := fmt.Errorf("ping yugabyte: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "runtime.yugabyte_ping_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// PingFoundationDB verifies that the FoundationDB stores can serve a request.
func (g *Graph) PingFoundationDB(ctx context.Context) error {
	if err := g.fdbStores.Ping(ctx); err != nil {
		wrapped := fmt.Errorf("ping foundationdb: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "runtime.foundationdb_ping_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// Close stops the delete resume loop and the search workers, then releases the search adapter, the
// audit runtime, and the Postgres pool, in that order.
func (g *Graph) Close() {
	if err := g.CloseContext(context.Background()); err != nil {
		slog.Error("runtime.close_failed", slog.String("err", err.Error()))
	}
}

// CloseContext stops the subtree delete resume loop, the model repair loop,
// and every search worker loop and waits for them before it closes the
// search adapter and the remaining dependencies.
func (g *Graph) CloseContext(ctx context.Context) error {
	g.stopDeleteResumer()
	g.stopSearchModelRepair()
	if g.searchCancel != nil {
		g.searchCancel()
	}
	if g.searchStarted {
		g.searchWorkers.Wait()
		telemetry.L(ctx).InfoContext(ctx, "search.worker.stopped", slog.Int("workers", len(g.search.workers)))
	}
	var closeErr error
	if g.search.adapter != nil {
		if err := g.search.adapter.Close(ctx); err != nil {
			closeErr = fmt.Errorf("close search adapter: %w", err)
		}
	}
	g.audit.Close()
	if g.pool != nil {
		g.pool.Close()
	}
	return closeErr
}
