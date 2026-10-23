package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

// searchLoadPointInTimeInterval sets the time between point-in-time samples during a run.
const searchLoadPointInTimeInterval = 5 * time.Second

// searchLoadIndexMetrics restricts each index statistics request to the search metric group.
var searchLoadIndexMetrics = []string{"search"}

// SearchLoadPointInTime reports samples of open point-in-time contexts across all shard copies
// of the index that the node-pages alias selects. Each sample reads point_in_time_current from
// the total section of the search statistics. Tack opens one point in time per search session.
type SearchLoadPointInTime struct {
	// Peak is the largest open context count among successful index statistics reads.
	Peak int
	// Answered counts successful index statistics reads.
	Answered int
	Samples  []SearchLoadPointInTimeSample
}

// SearchLoadPointInTimeSample records one index statistics read. Unavailable is true when the
// request fails, OpenSearch returns an HTTP error status, or the alias does not select exactly
// one index. Status records the HTTP response status, or zero when the request did not receive
// a response.
type SearchLoadPointInTimeSample struct {
	Offset      time.Duration
	Index       string
	Open        int
	Unavailable bool
	Status      int
	Error       string
}

type searchLoadIndexStats struct {
	Indices map[string]searchLoadIndexStat `json:"indices"`
}

type searchLoadIndexStat struct {
	Total searchLoadIndexTotals `json:"total"`
}

type searchLoadIndexTotals struct {
	Search searchLoadSearchStat `json:"search"`
}

type searchLoadSearchStat struct {
	PointInTimeCurrent int `json:"point_in_time_current"`
}

// searchLoadPointsInTime reads index statistics with the runtime OpenSearch credential.
type searchLoadPointsInTime struct {
	client  *opensearch.Client
	stop    chan struct{}
	done    chan struct{}
	started time.Time                     `exhaustruct:"optional"`
	mutex   sync.Mutex                    `exhaustruct:"optional"`
	samples []SearchLoadPointInTimeSample `exhaustruct:"optional"`
	logged  bool                          `exhaustruct:"optional"`
}

func newSearchLoadPointsInTime(ctx context.Context, cfg *config.Config) (*searchLoadPointsInTime, error) {
	client, err := newSearchStatisticsClient(ctx, cfg, "qa datagen search-load")
	if err != nil {
		return nil, err
	}
	return &searchLoadPointsInTime{client: client, stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// newSearchStatisticsClient creates an OpenSearch client with the configured search endpoint,
// CA certificate, runtime username and password, request timeout, and retry count. Each error
// message starts with the command name that the caller passes.
func newSearchStatisticsClient(ctx context.Context, cfg *config.Config, command string) (*opensearch.Client, error) {
	certificate, err := config.LoadSearchCA(ctx, cfg.SearchCA)
	if err != nil {
		return nil, loggedError(ctx, command+": the command could not load the search CA certificate", err)
	}
	settings := search.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate, Username: cfg.SearchUsername,
		Password: cfg.SearchPassword, RequestTimeout: cfg.SearchRequestTimeout, MaxRetries: cfg.SearchMaxRetries,
	}
	if err := settings.Validate(ctx); err != nil {
		return nil, loggedError(ctx, command+": the command could not validate the search settings", err)
	}
	client, err := opensearch.NewClient(settings.ClientConfig(nil))
	if err != nil {
		return nil, loggedError(ctx, command+": the command could not create an OpenSearch client", err)
	}
	return client, nil
}

// start records the first sample, then records one sample at each interval until finish stops
// sampling.
func (p *searchLoadPointsInTime) start(ctx context.Context) {
	p.started = clock.Now()
	p.record(ctx)
	go func() {
		defer close(p.done)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "qa.datagen.search_load_point_in_time_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		ticker := time.NewTicker(searchLoadPointInTimeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.record(ctx)
			}
		}
	}()
}

// finish stops interval sampling, records the last sample, and returns the report.
func (p *searchLoadPointsInTime) finish(ctx context.Context) SearchLoadPointInTime {
	close(p.stop)
	<-p.done
	p.record(ctx)
	p.mutex.Lock()
	defer p.mutex.Unlock()
	report := SearchLoadPointInTime{Peak: 0, Answered: 0, Samples: slices.Clone(p.samples)}
	for _, sample := range p.samples {
		if sample.Unavailable {
			continue
		}
		report.Answered++
		report.Peak = max(report.Peak, sample.Open)
	}
	return report
}

func (p *searchLoadPointsInTime) close(ctx context.Context) {
	if err := p.client.Close(); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_point_in_time_close_failed", slog.String("err", err.Error()))
	}
}

// record reads one sample and logs the first unavailable sample of the run at Error level.
func (p *searchLoadPointsInTime) record(ctx context.Context) {
	sample := p.read(ctx)
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if sample.Unavailable && !p.logged {
		p.logged = true
		slog.ErrorContext(ctx, "qa.datagen.search_load_point_in_time_unavailable", slog.String("err", sample.Error),
			slog.Int("status", sample.Status))
	}
	p.samples = append(p.samples, sample)
}

func (p *searchLoadPointsInTime) read(ctx context.Context) SearchLoadPointInTimeSample {
	sample := SearchLoadPointInTimeSample{
		Offset: clock.Now().Sub(p.started), Index: "", Open: 0, Unavailable: false, Status: 0, Error: "",
	}
	var stats searchLoadIndexStats
	request := opensearchapi.IndicesStatsReq{Indices: []string{search.PublicAlias}, Metrics: searchLoadIndexMetrics}
	response, err := opensearch.Do(ctx, p.client, http.MethodGet, request, &stats)
	if response != nil {
		sample.Status = response.StatusCode
	}
	switch {
	case err != nil:
		sample.Error = err.Error()
	case response.IsError():
		_ = response.Body.Close()
		sample.Error = fmt.Sprintf("index statistics request failed with HTTP status %d for alias %s", response.StatusCode, search.PublicAlias)
	case len(stats.Indices) != 1:
		sample.Error = fmt.Sprintf("alias %s selects %d indices instead of one", search.PublicAlias, len(stats.Indices))
	default:
		for index, stat := range stats.Indices {
			sample.Index, sample.Open = index, stat.Total.Search.PointInTimeCurrent
		}
		return sample
	}
	sample.Unavailable = true
	return sample
}
