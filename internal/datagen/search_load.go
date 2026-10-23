package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

// SearchLoadOptions specifies the search workload parameters.
type SearchLoadOptions struct {
	Seed        int64
	Rate        int
	Duration    time.Duration
	Concurrency int
	Commit      bool
	// Endpoints lists base URLs for HTTP tack_search calls to deployed Tack
	// processes. The workload builds and calls an in-process runtime graph
	// when Endpoints is empty.
	Endpoints []string `exhaustruct:"optional"`
	// Client sends HTTP tack_search requests to Endpoints.
	Client *http.Client `exhaustruct:"optional"`
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
	PointInTime     SearchLoadPointInTime
}

type searchLoad struct {
	drivers []*Driver
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
	drivers, closeDrivers, err := searchLoadDrivers(callContext, cfg, options)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	defer closeDrivers()
	load, err := newSearchLoad(callContext, cfg, options, drivers, planned)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	pointsInTime, err := newSearchLoadPointsInTime(callContext, cfg)
	if err != nil {
		return SearchLoadSummary{}, err
	}
	defer pointsInTime.close(callContext)
	pointsInTime.start(callContext)
	summary := load.run(scheduleContext, callContext, planned)
	summary.PointInTime = pointsInTime.finish(callContext)
	slog.InfoContext(callContext, "qa.datagen.search_load_finished", slog.Int("rate", options.Rate),
		slog.Int("endpoints", len(options.Endpoints)), slog.Int("sent", summary.Sent),
		slog.Int("completed", summary.Completed), slog.Int("dropped", summary.Dropped),
		slog.Int("unavailable", summary.Unavailable), slog.Int("point_in_time_peak", summary.PointInTime.Peak),
		slog.Duration("elapsed", summary.Elapsed))
	return summary, nil
}

func newSearchLoad(ctx context.Context, cfg *config.Config, options SearchLoadOptions, drivers []*Driver, planned int) (*searchLoad, error) {
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return nil, err
	}
	identities, err := BootstrapIdentities(ctx, cfg, options.Seed, scale)
	if err != nil {
		return nil, err
	}
	workspace := identities.Workspaces[0]
	queries, err := newSearchLoadQueries(ctx, options.Seed, workspace, planned)
	if err != nil {
		return nil, err
	}
	return &searchLoad{
		drivers: drivers, token: workspace.Actors[0].Token, entry: workspace.Slug,
		options: options, queries: queries, stats: &searchLoadStats{},
	}, nil
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
	if len(options.Endpoints) > 0 && options.Client == nil {
		return errors.New("qa datagen search-load: endpoints require an HTTP client")
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
		driver := l.drivers[sent%len(l.drivers)]
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
			l.send(callContext, driver, query)
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

func (l *searchLoad) send(ctx context.Context, driver *Driver, query string) {
	requestStarted := clock.Now()
	_, err := callSearch(ctx, driver, l.token, l.entry, query, "")
	l.stats.record(clock.Now().Sub(requestStarted), err)
}
