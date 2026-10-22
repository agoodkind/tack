package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

var errSearchMixedPhasePanic = errors.New("qa datagen search-mixed: workload phase panicked")

// run starts sampling, searches, workload phases, and scheduled writes.
// After the searches, phases, and writes finish, it waits for freshness probes.
// It then waits for search work to finish unless scheduleContext has ended, and stops sampling.
func (m *searchMixed) run(scheduleContext, callContext context.Context, plannedWrites int) (SearchMixedSummary, error) {
	probeContext, stopProbes := context.WithCancel(callContext)
	defer stopProbes()
	windowStarted := clock.Now()
	stopSampler, samplerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_sampler_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		m.sampleUntil(callContext, windowStarted, stopSampler, samplerDone)
	}()
	searches := make(chan SearchLoadSummary, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_load_panicked", slog.String("err", fmt.Sprint(recovered)))
				searches <- m.loadStats.summary(false, 0, 0, 0, 0, 0)
			}
		}()
		searches <- m.runLoads(scheduleContext, callContext)
	}()
	phaseErrors := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_phase_panicked", slog.String("err", fmt.Sprint(recovered)))
				phaseErrors <- errSearchMixedPhasePanic
			}
		}()
		phaseErrors <- m.runPhases(scheduleContext, callContext, windowStarted)
	}()
	writeElapsed := m.write(scheduleContext, callContext, probeContext, plannedWrites)
	searchSummary := <-searches
	phaseErr := <-phaseErrors
	writesEnded := clock.Now()
	m.drain(scheduleContext, callContext, stopProbes)
	if scheduleContext.Err() == nil {
		m.catchUp(scheduleContext, writesEnded)
	}
	close(stopSampler)
	<-samplerDone
	summary := m.stats.summary(searchSummary, plannedWrites, writeElapsed)
	summary.Endpoints, summary.Tenants = len(m.options.Endpoints), len(m.tenants)
	m.phases.apply(&summary)
	return summary, phaseErr
}

// runLoads runs the search load of every tenant at once and combines their counts.
func (m *searchMixed) runLoads(scheduleContext, callContext context.Context) SearchLoadSummary {
	results := make([]SearchLoadSummary, len(m.loads))
	planned := 0
	var loads sync.WaitGroup
	for index, load := range m.loads {
		loadPlanned := int(load.options.Duration * time.Duration(load.options.Rate) / time.Minute)
		planned += loadPlanned
		loads.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.ErrorContext(callContext, "qa.datagen.search_mixed_load_panicked", slog.String("err", fmt.Sprint(recovered)))
				}
			}()
			results[index] = load.run(scheduleContext, callContext, loadPlanned)
		})
	}
	loads.Wait()
	sent, dropped, distinct, elapsed := 0, 0, 0, time.Duration(0)
	for _, result := range results {
		sent, dropped, distinct = sent+result.Sent, dropped+result.Dropped, distinct+result.DistinctQueries
		elapsed = max(elapsed, result.Elapsed)
	}
	return m.loadStats.summary(false, planned, sent, dropped, distinct, elapsed)
}

func (m *searchMixed) write(scheduleContext, callContext, probeContext context.Context, planned int) time.Duration {
	interval := time.Minute / time.Duration(m.options.WriteRate)
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
			m.editIssue(callContext, probeContext, index, node)
			return
		}
	case searchMixedDeleteKind:
		if node := m.settledNode(false); node != nil {
			m.deleteIssue(callContext, probeContext, index, node)
			return
		}
	}
	m.createScheduledIssue(callContext, probeContext, index)
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
