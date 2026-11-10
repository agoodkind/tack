package datagen

import (
	"context"
	"fmt"
	"slices"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	searchMixedProbeInterval = time.Second
	searchMixedMarkerLabel   = "k"
)

// searchMixedMarker records a written issue marker that the prober has not
// found.
type searchMixedMarker struct {
	node      *searchMixedNode
	marker    string
	committed time.Time
	// searchedAfterCatchUp becomes true after the final probe pass searches this
	// marker without an error.
	searchedAfterCatchUp bool `exhaustruct:"optional"`
}

// searchMixedDeletion records a deleted issue that the prober has not verified.
type searchMixedDeletion struct {
	node   *searchMixedNode
	marker string
}

func (m *searchMixed) trackMarker(node *searchMixedNode, marker string, committed time.Time) {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.markers = append(
		m.markers,
		searchMixedMarker{node: node, marker: marker, committed: committed},
	)
}

func (m *searchMixed) trackDeletion(node *searchMixedNode, marker string) {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.deletions = append(m.deletions, searchMixedDeletion{node: node, marker: marker})
}

// drainWaiting returns true while a marker, deletion, metadata, or move check
// remains pending and the final probe pass has not finished.
func (m *searchMixed) drainWaiting() bool {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	pending := len(
		m.markers,
	) > 0 || len(m.deletions) > 0 || len(m.metadataChecks) > 0 || len(m.moveChecks) > 0
	return pending && !m.finalPassDone
}

// drain sets drainStarted after the command completes or skips the catch-up
// wait. The next probe pass that starts is the final pass. drain waits until
// all probe checks settle, the final pass finishes, or scheduleContext ends.
// The --max-wall-time limit, SIGINT, SIGTERM, or parent context termination can
// end scheduleContext. drain then calls stopProbes to cancel probing and waits
// for proberDone.
func (m *searchMixed) drain(
	scheduleContext context.Context,
	stopProbes context.CancelFunc,
	proberDone <-chan struct{},
) {
	m.probeMutex.Lock()
	m.drainStarted = true
	m.probeMutex.Unlock()
	for m.drainWaiting() {
		if !waitForSearchMixedPoll(scheduleContext) {
			break
		}
	}
	stopProbes()
	<-proberDone
}

func waitForSearchMixedPoll(ctx context.Context) bool {
	timer := time.NewTimer(searchMixedPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *searchMixed) probe(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	defer m.abandonProbes()
	defer func() {
		if recovered := recover(); recovered != nil {
			m.recordPanic(ctx, "qa.datagen.search_mixed_probe_panicked", fmt.Sprint(recovered))
		}
	}()
	ticker := time.NewTicker(searchMixedProbeInterval)
	defer ticker.Stop()
	for tick := 0; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		afterCatchUp := m.startProbePass()
		if m.options.ProbeAfterCatchUp && !afterCatchUp {
			continue
		}
		m.probeMarkers(ctx, tick, afterCatchUp)
		m.probeDeletions(ctx, tick)
		m.probeMetadata(ctx, tick)
		m.probeMoves(ctx, tick)
		m.finishProbePass(afterCatchUp)
	}
}

// startProbePass returns true after drain starts and identifies the pass as
// the final pass.
func (m *searchMixed) startProbePass() bool {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	return m.drainStarted
}

// finishProbePass records final-pass completion when afterCatchUp is true.
func (m *searchMixed) finishProbePass(afterCatchUp bool) {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	if afterCatchUp {
		m.finalPassDone = true
	}
}

// abandonProbes counts pending checks when the prober stops and clears the
// pending lists. It selects counters as follows:
//
//   - A marker that the final pass searched without an error increments
//     markersNotFound; any other marker increments markersUnsearched.
//   - A pending deletion increments deletesUnverified.
//   - A pending metadata check increments metadataNotFound.
//   - A pending move check increments movesUnverified.
func (m *searchMixed) abandonProbes() {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	for _, entry := range m.markers {
		if entry.searchedAfterCatchUp {
			m.stats.count(&m.stats.markersNotFound)
		} else {
			m.stats.count(&m.stats.markersUnsearched)
		}
	}
	for range m.deletions {
		m.stats.count(&m.stats.deletesUnverified)
	}
	for range m.metadataChecks {
		m.stats.count(&m.stats.metadataNotFound)
	}
	for range m.moveChecks {
		m.stats.count(&m.stats.movesUnverified)
	}
	m.markers, m.deletions, m.metadataChecks, m.moveChecks = nil, nil, nil, nil
}

// probeMarkers searches each pending marker with a separate public tack_search
// query under its tenant, one search at a time. The "and" lexical operator
// scores a page only when it contains every query term.
//
// If the first result page contains the issue's node ID, probeMarkers records
// the freshness lag and removes the marker. When afterCatchUp is true, it sets
// searchedAfterCatchUp for remaining markers that it searched without an error.
func (m *searchMixed) probeMarkers(ctx context.Context, tick int, afterCatchUp bool) {
	m.probeMutex.Lock()
	pending := slices.Clone(m.markers)
	m.probeMutex.Unlock()
	found := make(map[*searchMixedNode]string)
	searched := make(map[*searchMixedNode]string)
	for _, entry := range pending {
		tenant := m.tenants[entry.node.tenant]
		driver := m.driver(tick + entry.node.ordinal)
		page, err := callSearch(
			ctx,
			driver,
			m.identities(entry.node.tenant, driver),
			tenant.token(),
			tenant.entry(),
			entry.marker,
			"",
		)
		if err != nil {
			m.stats.count(&m.stats.probeErrors)
			continue
		}
		searched[entry.node] = entry.marker
		if slices.Contains(page.IDs, entry.node.id) {
			m.stats.markerFound(clock.Now().Sub(entry.committed))
			m.settle(entry.node, entry.marker)
			found[entry.node] = entry.marker
		}
	}
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.markers = slices.DeleteFunc(
		m.markers,
		func(entry searchMixedMarker) bool { return found[entry.node] == entry.marker },
	)
	for index := range m.markers {
		entry := &m.markers[index]
		if afterCatchUp && searched[entry.node] == entry.marker {
			entry.searchedAfterCatchUp = true
		}
	}
}
