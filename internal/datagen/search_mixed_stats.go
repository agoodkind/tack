package datagen

import (
	"slices"
	"sync"
	"time"
)

type searchMixedStats struct {
	mutex             sync.Mutex      `exhaustruct:"optional"`
	lags              []time.Duration `exhaustruct:"optional"`
	creates           int             `exhaustruct:"optional"`
	edits             int             `exhaustruct:"optional"`
	deletes           int             `exhaustruct:"optional"`
	writeErrors       int             `exhaustruct:"optional"`
	markersNotFound   int             `exhaustruct:"optional"`
	deletedReturned   int             `exhaustruct:"optional"`
	deletesUnverified int             `exhaustruct:"optional"`
}

func (s *searchMixedStats) count(counter *int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	*counter++
}

func (s *searchMixedStats) markerFound(lag time.Duration) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.lags = append(s.lags, lag)
}

func (s *searchMixedStats) summary(searches SearchLoadSummary, plannedWrites int, writeElapsed time.Duration) SearchMixedSummary {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	sorted := slices.Clone(s.lags)
	slices.Sort(sorted)
	freshness := SearchMixedFreshness{P50: 0, P95: 0, P99: 0, Max: 0}
	if len(sorted) > 0 {
		freshness = SearchMixedFreshness{
			P50: nearestRank(sorted, searchLoadP50), P95: nearestRank(sorted, searchLoadP95),
			P99: nearestRank(sorted, searchLoadP99), Max: sorted[len(sorted)-1],
		}
	}
	return SearchMixedSummary{
		Search: searches, PlannedWrites: plannedWrites, Creates: s.creates, Edits: s.edits, Deletes: s.deletes,
		WriteErrors: s.writeErrors, WriteElapsed: writeElapsed, Freshness: freshness,
		MarkersNotFound: s.markersNotFound, DeletedReturned: s.deletedReturned, DeletesUnverified: s.deletesUnverified,
	}
}
