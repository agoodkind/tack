package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	searchMixedDrainTimeout = 2 * time.Minute
	searchMixedPollInterval = 250 * time.Millisecond
	searchMixedWriteKinds   = 3
	searchMixedEditKind     = 1
	searchMixedDeleteKind   = 2
)

func (m *searchMixed) run(scheduleContext, callContext context.Context, plannedSearches, plannedWrites int) SearchMixedSummary {
	probeContext, stopProbes := context.WithCancel(callContext)
	defer stopProbes()
	searches := make(chan SearchLoadSummary, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_load_panicked", slog.String("err", fmt.Sprint(recovered)))
				searches <- m.load.stats.summary(false, plannedSearches, 0, 0, 0, 0)
			}
		}()
		searches <- m.load.run(scheduleContext, callContext, plannedSearches)
	}()
	writeElapsed := m.write(scheduleContext, callContext, probeContext, plannedWrites)
	searchSummary := <-searches
	m.drain(scheduleContext, callContext, stopProbes)
	return m.stats.summary(searchSummary, plannedWrites, writeElapsed)
}

func (m *searchMixed) write(scheduleContext, callContext, probeContext context.Context, planned int) time.Duration {
	interval := time.Minute / time.Duration(m.writeRate)
	started := clock.Now()
	for index := range planned {
		if !waitForSearchLoadStart(scheduleContext, started.Add(time.Duration(index)*interval)) {
			break
		}
		m.writeOne(callContext, probeContext, index)
	}
	return clock.Now().Sub(started)
}

func (m *searchMixed) writeOne(callContext, probeContext context.Context, index int) {
	switch index % searchMixedWriteKinds {
	case searchMixedEditKind:
		if node := m.settledNode(true); node != nil {
			m.editIssue(callContext, probeContext, node)
			return
		}
	case searchMixedDeleteKind:
		if node := m.settledNode(false); node != nil {
			m.deleteIssue(callContext, probeContext, node)
			return
		}
	}
	m.createIssue(callContext, probeContext)
}

func (m *searchMixed) probe(ctx context.Context, check func(context.Context)) {
	m.probes.Add(1)
	go func() {
		defer m.probes.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "qa.datagen.search_mixed_probe_panicked", slog.String("err", fmt.Sprint(recovered)))
				m.stats.count(&m.stats.markersNotFound)
			}
		}()
		check(ctx)
	}()
}

func (m *searchMixed) drain(scheduleContext, callContext context.Context, stopProbes context.CancelFunc) {
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_drain_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		m.probes.Wait()
	}()
	timer := time.NewTimer(searchMixedDrainTimeout)
	defer timer.Stop()
	select {
	case <-finished:
		return
	case <-timer.C:
	case <-scheduleContext.Done():
	}
	stopProbes()
	<-finished
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
