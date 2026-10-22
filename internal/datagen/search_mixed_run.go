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
// After the searches, phases, and writes finish, it waits for search work to finish
// unless its schedule context has ended. Freshness probes search for markers during this wait.
// It then waits up to two minutes for pending probes or until the schedule context ends.
// If a workload phase returns an error or panics, run cancels its schedule context.
// The cancellation stops search loads and scheduled writes.
// run skips the catch-up wait, and drain ends its probe wait at the next poll.
// run stops probes and sampling, then returns the summary and the phase error.
func (m *searchMixed) run(parentSchedule, callContext context.Context, plannedWrites int) (SearchMixedSummary, error) {
	scheduleContext, stopSchedule := context.WithCancel(parentSchedule)
	defer stopSchedule()
	probeContext, stopProbes := context.WithCancel(callContext)
	defer stopProbes()
	proberDone := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(callContext, "qa.datagen.search_mixed_probe_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		m.probe(probeContext, proberDone)
	}()
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
				stopSchedule()
				phaseErrors <- errSearchMixedPhasePanic
			}
		}()
		phaseErr := m.runPhases(scheduleContext, callContext, windowStarted)
		if phaseErr != nil {
			stopSchedule()
		}
		phaseErrors <- phaseErr
	}()
	writeElapsed := m.write(scheduleContext, callContext, plannedWrites)
	searchSummary := <-searches
	phaseErr := <-phaseErrors
	writesEnded := clock.Now()
	if scheduleContext.Err() == nil {
		m.catchUp(scheduleContext, writesEnded)
	}
	m.drain(scheduleContext, stopProbes, proberDone)
	close(stopSampler)
	<-samplerDone
	summary := m.stats.summary(searchSummary, plannedWrites, writeElapsed)
	summary.Endpoints, summary.Tenants, summary.Measured = len(m.options.Endpoints), len(m.tenants), true
	m.phases.apply(&summary)
	return summary, phaseErr
}

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

func (m *searchMixed) write(scheduleContext, callContext context.Context, planned int) time.Duration {
	interval := time.Minute / time.Duration(m.options.WriteRate)
	started := clock.Now()
	for index := range planned {
		if !waitForSearchLoadStart(scheduleContext, started.Add(time.Duration(index)*interval)) {
			break
		}
		m.writeOne(callContext, index)
	}
	return clock.Now().Sub(started)
}

func (m *searchMixed) writeOne(callContext context.Context, index int) {
	switch index % searchMixedWriteKinds {
	case searchMixedEditKind:
		if node := m.settledNode(true); node != nil {
			m.editIssue(callContext, index, node)
			return
		}
	case searchMixedDeleteKind:
		if node := m.settledNode(false); node != nil {
			m.deleteIssue(callContext, index, node)
			return
		}
	}
	m.createScheduledIssue(callContext, index)
}

// drain waits until the prober finds every pending marker and verifies every pending deletion,
// the two-minute timeout expires, or scheduleContext ends. It then stops the prober and waits
// for it to finish.
func (m *searchMixed) drain(scheduleContext context.Context, stopProbes context.CancelFunc, proberDone <-chan struct{}) {
	deadline := clock.Now().Add(searchMixedDrainTimeout)
	for m.probesPending() && clock.Now().Before(deadline) {
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
