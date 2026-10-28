package datagen

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	searchMixedProbeInterval = time.Second
	searchMixedProbeBatch    = 4
	searchMixedMarkerLabel   = "k"
)

// searchMixedMarker records a written issue marker that the prober has not yet found.
type searchMixedMarker struct {
	node      *searchMixedNode
	marker    string
	committed time.Time
}

// searchMixedDeletion records a deleted issue that the prober has not yet verified.
type searchMixedDeletion struct {
	node   *searchMixedNode
	marker string
}

func (m *searchMixed) trackMarker(node *searchMixedNode, marker string, committed time.Time) {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.markers = append(m.markers, searchMixedMarker{node: node, marker: marker, committed: committed})
}

func (m *searchMixed) trackDeletion(node *searchMixedNode, marker string) {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.deletions = append(m.deletions, searchMixedDeletion{node: node, marker: marker})
}

func (m *searchMixed) probesPending() bool {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	return len(m.markers) > 0 || len(m.deletions) > 0
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
		m.probeMarkers(ctx, tick)
		m.probeDeletions(ctx, tick)
	}
}

// abandonProbes counts pending markers as not found and pending deletions as unverified when probing stops.
func (m *searchMixed) abandonProbes() {
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	for range m.markers {
		m.stats.count(&m.stats.markersNotFound)
	}
	for range m.deletions {
		m.stats.count(&m.stats.deletesUnverified)
	}
	m.markers, m.deletions = nil, nil
}

func (m *searchMixed) probeMarkers(ctx context.Context, tick int) {
	m.probeMutex.Lock()
	pending := slices.Clone(m.markers)
	m.probeMutex.Unlock()
	found := make(map[*searchMixedNode]string)
	for tenantIndex, tenant := range m.tenants {
		owned := slices.DeleteFunc(slices.Clone(pending), func(entry searchMixedMarker) bool { return entry.node.tenant != tenantIndex })
		for batch := range slices.Chunk(owned, searchMixedProbeBatch) {
			words := make([]string, 0, len(batch))
			for _, entry := range batch {
				words = append(words, entry.marker)
			}
			driver := m.driver(tick + tenantIndex)
			page, err := callSearch(ctx, driver, m.identities(tenantIndex, driver), tenant.token(), tenant.entry(), strings.Join(words, " "), "")
			if err != nil {
				m.stats.count(&m.stats.probeErrors)
				continue
			}
			for _, entry := range batch {
				if slices.Contains(page.IDs, entry.node.id) {
					m.stats.markerFound(clock.Now().Sub(entry.committed))
					m.settle(entry.node, entry.marker)
					found[entry.node] = entry.marker
				}
			}
		}
	}
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.markers = slices.DeleteFunc(m.markers, func(entry searchMixedMarker) bool { return found[entry.node] == entry.marker })
}
