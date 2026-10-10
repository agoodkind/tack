package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

// SearchLoadOptions specifies the search workload parameters.
type SearchLoadOptions struct {
	Seed int64
	Rate        int
	Duration    time.Duration
	Concurrency int
	Commit      bool
}

// SearchLoadSummary reports request counts, errors, elapsed time, and latency.
type SearchLoadSummary struct {
	DryRun          bool
	Planned         int
	Sent            int
	Completed       int
	Dropped         int
	DistinctQueries int
	Unavailable     int
	ToolErrors      int
	TransportErrors int
	Elapsed         time.Duration
	Latency         SearchLoadLatency
}

type searchLoad struct {
	driver  *Driver
	token   string
	entry   string
	options SearchLoadOptions
	queries *searchLoadQueries
	stats   *searchLoadStats
}

// RunSearchLoad sends first-page tack_search requests only when Commit is true.
func RunSearchLoad(
	scheduleContext context.Context,
	callContext context.Context,
	cfg *config.Config,
	options SearchLoadOptions,
) (SearchLoadSummary, error) {
	if err := validateSearchLoadOptions(options); err != nil {
		return SearchLoadSummary{}, err
	}
	planned := int(options.Duration * time.Duration(options.Rate) / time.Minute)
	if !options.Commit {
		stats := &searchLoadStats{}
		return stats.summary(true, planned, 0, 0, 0, 0), nil
	}
	if planned < 1 {
		return SearchLoadSummary{}, fmt.Errorf("qa datagen search-load: rate and duration must schedule at least one request: rate %d, duration %s", options.Rate, options.Duration)
	}
	if err := ValidateTarget(cfg); err != nil {
		return SearchLoadSummary{}, err
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	graph, err := runtime.BuildGraph(callContext, cfg)
	if err != nil {
		return SearchLoadSummary{}, loggedError(callContext, "qa datagen search-load: build runtime", err)
	}
	defer graph.Close()
	identities, err := BootstrapIdentities(callContext, cfg, options.Seed, scale)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	workspace := identities.Workspaces[0]
	queries, err := newSearchLoadQueries(callContext, options.Seed, workspace, planned)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	load := &searchLoad{
		driver: NewDriver(graph, false, options.Seed), token: workspace.Actors[0].Token, entry: workspace.Slug,
		options: options, queries: queries, stats: &searchLoadStats{},
	}
	summary := load.run(scheduleContext, callContext, planned)
	slog.InfoContext(callContext, "qa.datagen.search_load_finished", slog.Int("rate", options.Rate),
		slog.Int("sent", summary.Sent), slog.Int("completed", summary.Completed), slog.Int("dropped", summary.Dropped),
		slog.Duration("elapsed", summary.Elapsed))
	return summary, nil
}

func validateSearchLoadOptions(options SearchLoadOptions) error {
	if options.Duration <= 0 {
		return errors.New("qa datagen search-load: duration must be positive")
	}
	if options.Concurrency <= 0 {
		return errors.New("qa datagen search-load: concurrency must be positive")
	}
	if options.Rate < 0 {
		return errors.New("qa datagen search-load: rate must not be negative")
	}
	if options.Commit && options.Rate == 0 {
		return errors.New("qa datagen search-load: rate must be positive with --commit")
	}
	return nil
}

func (l *searchLoad) run(scheduleContext, callContext context.Context, planned int) SearchLoadSummary {
	interval := time.Minute / time.Duration(l.options.Rate)
	slots := make(chan struct{}, l.options.Concurrency)
	distinct := make(map[string]struct{}, planned)
	var inFlight sync.WaitGroup
	started := clock.Now()
	sent, dropped := 0, 0
	for index := range planned {
		if !waitForSearchLoadStart(scheduleContext, started.Add(time.Duration(index)*interval)) {
			break
		}
		select {
		case slots <- struct{}{}:
		default:
			dropped++
			continue
		}
		query := l.queries.text(sent)
		distinct[query] = struct{}{}
		sent++
		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			defer func() { <-slots }()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.ErrorContext(callContext, "qa.datagen.search_load_request_panicked", slog.String("err", fmt.Sprint(recovered)))
					l.stats.record(0, errSearchLoadPanic)
				}
			}()
			l.send(callContext, query)
		}()
	}
	inFlight.Wait()
	elapsed := clock.Now().Sub(started)
	return l.stats.summary(false, planned, sent, dropped, len(distinct), elapsed)
}

func waitForSearchLoadStart(ctx context.Context, startAt time.Time) bool {
	delay := startAt.Sub(clock.Now())
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (l *searchLoad) send(ctx context.Context, query string) {
	requestStarted := clock.Now()
	_, err := callSearch(ctx, l.driver, l.token, l.entry, query, "")
	l.stats.record(clock.Now().Sub(requestStarted), err)
}
