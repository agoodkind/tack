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
	probeErrors       int             `exhaustruct:"optional"`
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
		ProbeErrors: s.probeErrors, Endpoints: 0, Tenants: 0, Burst: SearchMixedBurst{Planned: 0, Created: 0, Failed: 0, Started: 0, Elapsed: 0},
		Sessions: SearchMixedSessions{
			Checks: 0, Pages: 0, Replays: 0, Endpoints: 0, Unavailable: 0, ContinuationErrors: 0, MissingCorpusIssues: 0, ReplayMismatches: 0,
			CrossTenantResults: 0, DeletedNodesReturned: 0,
		},
		Pending: SearchMixedPending{Checks: 0, PendingObserved: 0, Unavailable: 0, DeletedNodesReturned: 0},
		Revocation: SearchMixedRevocation{
			Checks: 0, PagesWithinCacheLifetime: 0, ResultsAfterCacheLifetime: 0, Unavailable: 0, RefusedAfter: 0, Refused: false,
		},
		Rebuild: SearchMixedRebuildResult{Requested: false, TargetIndex: "", Started: 0, Switched: 0, FinalState: ""},
		CatchUp: SearchMixedCatchUp{Completed: false, Elapsed: 0},
		Samples: []SearchMixedSample{}, Measured: false, SkippedPhases: []string{},
	}
}

// searchMixedPhaseStats collects the results of the workload phases.
type searchMixedPhaseStats struct {
	mutex      sync.Mutex               `exhaustruct:"optional"`
	burst      SearchMixedBurst         `exhaustruct:"optional"`
	sessions   SearchMixedSessions      `exhaustruct:"optional"`
	pending    SearchMixedPending       `exhaustruct:"optional"`
	revocation SearchMixedRevocation    `exhaustruct:"optional"`
	rebuild    SearchMixedRebuildResult `exhaustruct:"optional"`
	catchUp    SearchMixedCatchUp       `exhaustruct:"optional"`
	samples    []SearchMixedSample      `exhaustruct:"optional"`
	skipped    []string                 `exhaustruct:"optional"`
}

func (p *searchMixedPhaseStats) update(change func(*searchMixedPhaseStats)) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	change(p)
}

func (p *searchMixedPhaseStats) apply(summary *SearchMixedSummary) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	summary.Burst, summary.Sessions, summary.Pending = p.burst, p.sessions, p.pending
	summary.Revocation, summary.Rebuild, summary.CatchUp = p.revocation, p.rebuild, p.catchUp
	summary.Samples, summary.SkippedPhases = slices.Clone(p.samples), slices.Clone(p.skipped)
}
