package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// searchRuntime is the adapter, workers, and idle interval of the durable
// indexing loops.
type searchRuntime struct {
	adapter  *searchadapter.Adapter
	workers  []*service.SearchWorker
	idleTime time.Duration
}

// buildSearchRuntime constructs the content reader, policy set, work store,
// access store, adapter, and workers on the graph's clock. It returns an
// empty runtime when no search endpoint is configured.
func buildSearchRuntime(ctx context.Context, cfg *config.Config, stores *fdbadapter.Stores, source clock.Clock) (searchRuntime, error) {
	empty := searchRuntime{adapter: nil, workers: nil, idleTime: 0}
	if cfg.SearchEndpoint == "" {
		return empty, nil
	}
	settings, err := config.LoadSearchWorkerSettings(ctx)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search worker settings", err)
	}
	certificate, err := config.LoadSearchCA(ctx, cfg.SearchCA)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search CA", err)
	}
	pass := cfg.SearchPassword
	adapter, err := searchadapter.New(ctx, searchadapter.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate, Username: cfg.SearchUsername,
		Password: pass, RequestTimeout: cfg.SearchRequestTimeout, MaxRetries: cfg.SearchMaxRetries,
	})
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "create OpenSearch adapter", err)
	}
	policies := stores.SearchPolicySet()
	ports := service.SearchWorkerPorts{
		Store: stores.SearchWork(source), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: adapter,
	}
	workers := make([]*service.SearchWorker, 0, settings.Concurrency)
	for position := range settings.Concurrency {
		worker, err := service.NewSearchWorker(ports, source, settings, position)
		if err != nil {
			return empty, searchRuntimeFailure(ctx, "create search worker "+strconv.Itoa(position), errors.Join(err, adapter.Close(ctx)))
		}
		workers = append(workers, worker)
	}
	return searchRuntime{adapter: adapter, workers: workers, idleTime: settings.IdleInterval}, nil
}

func searchRuntimeFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("runtime: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.runtime.build_failed", slog.String("err", wrapped.Error()))
	return wrapped
}

// StartSearchWorkers starts one durable indexing loop per configured worker.
// Every loop stops when ctx ends or CloseContext runs.
func (g *Graph) StartSearchWorkers(ctx context.Context) {
	if len(g.search.workers) == 0 || g.searchStarted {
		return
	}
	g.searchStarted = true
	workerContext, cancel := context.WithCancel(ctx)
	g.searchCancel = cancel
	for _, worker := range g.search.workers {
		current := worker
		g.searchWorkers.Go(func() {
			defer recoverSearchWorker(workerContext, "search.worker.loop_panicked")
			runSearchLoop(workerContext, current, g.search.idleTime)
		})
	}
	telemetry.L(ctx).InfoContext(ctx, "search.worker.started", slog.Int("workers", len(g.search.workers)))
}

// runSearchLoop runs slices until ctx ends. It waits the idle interval after
// an idle or failed slice.
func runSearchLoop(ctx context.Context, worker *service.SearchWorker, idle time.Duration) {
	for ctx.Err() == nil {
		if runSearchIteration(ctx, worker) {
			continue
		}
		timer := time.NewTimer(idle)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// runSearchIteration runs one slice and recovers a panic in that slice. The
// loop then continues with the next slice.
func runSearchIteration(ctx context.Context, worker *service.SearchWorker) (worked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.worker.slice_panicked", slog.String("err", fmt.Sprint(recovered)))
			worked = false
		}
	}()
	claimed, err := worker.RunSlice(ctx)
	return claimed && err == nil
}

func recoverSearchWorker(ctx context.Context, event string) {
	if recovered := recover(); recovered != nil {
		telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", fmt.Sprint(recovered)))
	}
}
