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

// searchMixedCreateWorkers limits concurrent public issue creation calls during setup and the
// ingestion burst.
const searchMixedCreateWorkers = 8

var errSearchMixedCreatePanic = errors.New("qa datagen search-mixed: create call panicked")

// runCreates calls create once for each integer from zero up to count, excluding count.
// It uses at most searchMixedCreateWorkers concurrent workers and returns every failure.
func (m *searchMixed) runCreates(ctx context.Context, count int, create func(context.Context, int) error) []error {
	numbers := make(chan int)
	var failureMutex sync.Mutex
	failures := make([]error, 0)
	record := func(err error) {
		failureMutex.Lock()
		defer failureMutex.Unlock()
		failures = append(failures, err)
	}
	var workers sync.WaitGroup
	for range min(searchMixedCreateWorkers, max(count, 1)) {
		workers.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.ErrorContext(ctx, "qa.datagen.search_mixed_create_panicked", slog.String("err", fmt.Sprint(recovered)))
					record(errSearchMixedCreatePanic)
				}
			}()
			for number := range numbers {
				if err := create(ctx, number); err != nil {
					record(err)
				}
			}
		})
	}
	for number := range count {
		numbers <- number
	}
	close(numbers)
	workers.Wait()
	return failures
}

// burst creates the planned burst issues back to back in alternating tenants.
func (m *searchMixed) burst(ctx context.Context, windowStarted time.Time) {
	planned := m.options.BurstWrites
	started := clock.Now()
	failures := m.runCreates(ctx, planned, func(ctx context.Context, number int) error {
		text := fmt.Sprintf("Burst %s issue %d", m.runKey, number)
		if _, err := m.createIssue(ctx, number%len(m.tenants), number, text); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_burst_create_failed", slog.String("err", err.Error()))
			return err
		}
		return nil
	})
	elapsed := clock.Now().Sub(started)
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.burst = SearchMixedBurst{
			Planned: planned, Created: planned - len(failures), Failed: len(failures),
			Started: started.Sub(windowStarted), Elapsed: elapsed,
		}
	})
	slog.InfoContext(ctx, "qa.datagen.search_mixed_burst_finished", slog.Int("planned", planned),
		slog.Int("failed", len(failures)), slog.Duration("elapsed", elapsed))
}
