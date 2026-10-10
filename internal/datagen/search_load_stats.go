package datagen

import (
	"errors"
	"slices"
	"sync"
	"time"
)

const (
	searchLoadP50 = 50
	searchLoadP95 = 95
	searchLoadP99 = 99
)

var errSearchLoadPanic = errors.New("qa datagen search-load: request panicked")

// SearchLoadLatency reports latency bounds and percentiles for successful search requests.
type SearchLoadLatency struct {
	Min time.Duration
	P50 time.Duration
	P95 time.Duration
	P99 time.Duration
	Max time.Duration
}

type searchLoadStats struct {
	mutex           sync.Mutex      `exhaustruct:"optional"`
	latencies       []time.Duration `exhaustruct:"optional"`
	unavailable     int             `exhaustruct:"optional"`
	toolErrors      int             `exhaustruct:"optional"`
	transportErrors int             `exhaustruct:"optional"`
}

func (s *searchLoadStats) record(elapsed time.Duration, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	var toolError *toolCallError
	switch {
	case err == nil:
		s.latencies = append(s.latencies, elapsed)
	case !errors.As(err, &toolError):
		s.transportErrors++
	case toolError.message == soakSearchUnavailableText:
		s.unavailable++
	default:
		s.toolErrors++
	}
}

func (s *searchLoadStats) summary(dryRun bool, planned, sent, dropped, distinct int, elapsed time.Duration) SearchLoadSummary {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	sorted := slices.Clone(s.latencies)
	slices.Sort(sorted)
	latency := SearchLoadLatency{Min: 0, P50: 0, P95: 0, P99: 0, Max: 0}
	if len(sorted) > 0 {
		latency = SearchLoadLatency{
			Min: sorted[0], P50: nearestRank(sorted, searchLoadP50), P95: nearestRank(sorted, searchLoadP95),
			P99: nearestRank(sorted, searchLoadP99), Max: sorted[len(sorted)-1],
		}
	}
	return SearchLoadSummary{
		DryRun: dryRun, Planned: planned, Sent: sent, Completed: len(sorted), Dropped: dropped,
		DistinctQueries: distinct, Unavailable: s.unavailable, ToolErrors: s.toolErrors,
		TransportErrors: s.transportErrors, Elapsed: elapsed, Latency: latency,
	}
}
