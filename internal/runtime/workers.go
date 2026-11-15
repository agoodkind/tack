package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

func buildSearchWorkers(
	ctx context.Context,
	ports service.SearchWorkerPorts,
	source clock.Clock,
	settings config.SearchWorkerSettings,
	adapter *searchadapter.Adapter,
) ([]*service.SearchWorker, error) {
	workers := make([]*service.SearchWorker, 0, settings.Concurrency)
	for position := range settings.Concurrency {
		worker, err := service.NewSearchWorker(ports, source, settings, position)
		if err != nil {
			return nil, searchRuntimeFailure(
				ctx,
				"create search worker "+strconv.Itoa(position),
				errors.Join(err, adapter.Close(ctx)),
			)
		}

		workers = append(workers, worker)
	}

	return workers, nil
}

// StartSearchWorkers starts one durable indexing loop per configured worker.
//
// Every loop stops when ctx ends or CloseContext runs. A repeated call does
// not start another set of workers.
//
// For example:
//
//	graph.StartSearchWorkers(ctx)
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

	g.searchWorkers.Go(func() {
		defer recoverSearchWorker(workerContext, "search.retention.loop_panicked")
		runRetentionLoop(workerContext, g.search.retention)
	})
	telemetry.L(ctx).InfoContext(
		ctx,
		"search.worker.started",
		slog.Int("workers", len(g.search.workers)),
	)
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
func runSearchIteration(ctx context.Context, worker *service.SearchWorker) bool {
	worked := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				telemetry.L(ctx).ErrorContext(
					ctx,
					"search.worker.slice_panicked",
					slog.String("err", fmt.Sprint(recovered)),
				)
				worked = false
			}
		}()
		claimed, err := worker.RunSlice(ctx)
		worked = claimed && err == nil
	}()

	return worked
}

// runRetentionLoop checks the retention thresholds once per interval until
// ctx ends. A failed check logs and waits for the next interval.
func runRetentionLoop(ctx context.Context, retention *service.SearchRetention) {
	ticker := time.NewTicker(retention.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkRetention(ctx, retention)
		}
	}
}

// checkRetention runs one check and recovers a panic in that check. It
// drops the returned error. Check already logs each failure at Error.
func checkRetention(ctx context.Context, retention *service.SearchRetention) {
	defer recoverSearchWorker(ctx, "search.retention.check_panicked")
	_ = retention.Check(ctx)
}

func recoverSearchWorker(ctx context.Context, event string) {
	if recovered := recover(); recovered != nil {
		telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", fmt.Sprint(recovered)))
	}
}
