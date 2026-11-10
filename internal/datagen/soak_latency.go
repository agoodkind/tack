package datagen

import (
	"maps"
	"slices"
	"time"
)

// SoakOperationLatency records the completed call count and latency for one
// soak operation kind. P50 and P95 use nearest-rank percentiles of each
// completed operation's wall time, including all its MCP calls. A nearest-rank
// percentile selects an observed duration at the requested rank. Min and Max
// record the shortest and longest wall times.
type SoakOperationLatency struct {
	Kind  string
	Calls int
	Min   time.Duration
	P50   time.Duration
	P95   time.Duration
	Max   time.Duration
}

// soakLatencies stores the wall time of every completed operation by kind.
// Its add method accepts the zero value without prior initialization.
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
		latencies = append(
			latencies,
			SoakOperationLatency{
				Kind:  kind,
				Calls: len(sorted),
				Min:   sorted[0],
				P50:   nearestRank(sorted, 50),
				P95:   nearestRank(sorted, 95),
			Max: sorted[len(sorted)-1],
			},
		)
	}
	return latencies
}

// nearestRank returns the smallest value in sorted with at least percentile
// percent of the values at or below it. The sorted slice must contain at least
// one value and use ascending order.
func nearestRank(sorted []time.Duration, percentile int) time.Duration {
	rank := (percentile*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
