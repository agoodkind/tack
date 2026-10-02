package datagen

import (
	"maps"
	"slices"
	"time"
)

// SoakOperationLatency is the completed call count and latency of one soak
// operation kind. P50 and P95 are nearest-rank percentiles of the wall time
// of each completed operation, which includes all of its MCP calls.
type SoakOperationLatency struct {
	Kind  string
	Calls int
	P50   time.Duration
	P95   time.Duration
}

// soakLatencies stores the wall time of every completed operation by kind.
// The zero value is ready to use.
type soakLatencies struct {
	samples map[string][]time.Duration
}

func (l *soakLatencies) add(kind string, elapsed time.Duration) {
	if l.samples == nil {
		l.samples = make(map[string][]time.Duration)
	}
	l.samples[kind] = append(l.samples[kind], elapsed)
}

// summary returns one entry per recorded kind, sorted by kind.
func (l *soakLatencies) summary() []SoakOperationLatency {
	kinds := slices.Sorted(maps.Keys(l.samples))
	latencies := make([]SoakOperationLatency, 0, len(kinds))
	for _, kind := range kinds {
		sorted := slices.Clone(l.samples[kind])
		slices.Sort(sorted)
		latencies = append(latencies, SoakOperationLatency{
			Kind: kind, Calls: len(sorted),
			P50: nearestRank(sorted, 50), P95: nearestRank(sorted, 95),
		})
	}
	return latencies
}

// nearestRank returns the percentile of sorted, a non-empty ascending slice:
// the smallest value with at least percentile percent of the values at or
// below it.
func nearestRank(sorted []time.Duration, percentile int) time.Duration {
	rank := (percentile*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
