package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	searchMixedSampleInterval = 2 * time.Second
	// searchMixedNoRebuild is the sample state when FoundationDB does not record an index rebuild.
	searchMixedNoRebuild = "none"
	// searchMixedRebuildFinished is the sample state after the rebuild record disappears.
	searchMixedRebuildFinished = "finished"
)

// searchMixedCatchUpClasses returns the work classes for issue writes and permission changes.
func searchMixedCatchUpClasses() []searchdomain.WorkClass {
	return []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup}
}

// sampleUntil records one sample every searchMixedSampleInterval until stop closes.
func (m *searchMixed) sampleUntil(ctx context.Context, windowStarted time.Time, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_sampler_panicked", slog.String("err", fmt.Sprint(recovered)))
		}
	}()
	ticker := time.NewTicker(searchMixedSampleInterval)
	defer ticker.Stop()
	for {
		m.recordSample(ctx, windowStarted)
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func (m *searchMixed) recordSample(ctx context.Context, windowStarted time.Time) {
	sample := SearchMixedSample{
		Offset: clock.Now().Sub(windowStarted), OldestPendingWork: 0, OldestClass: "",
		RebuildState: "", Nodes: nil, Error: "",
	}
	problems := make([]string, 0)
	oldest, err := m.backlog.oldest(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	}
	now := clock.Now()
	for class, enqueued := range oldest {
		if class != searchdomain.WorkClassRebuild && now.Sub(enqueued) > sample.OldestPendingWork {
			sample.OldestPendingWork, sample.OldestClass = now.Sub(enqueued), string(class)
		}
	}
	sample.RebuildState, err = m.rebuildState(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	}
	sample.Nodes, err = m.resources.read(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	}
	sample.Error = strings.Join(problems, "; ")
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.samples = append(phases.samples, sample)
		phases.rebuild = rebuildProgress(phases.rebuild, sample)
	})
}

// rebuildProgress records the latest rebuild state and the elapsed time when a sample first
// reports a retiring or finished rebuild.
func rebuildProgress(rebuild SearchMixedRebuildResult, sample SearchMixedSample) SearchMixedRebuildResult {
	if !rebuild.Requested || sample.Offset < rebuild.Started || sample.RebuildState == "" {
		return rebuild
	}
	state := sample.RebuildState
	if state == searchMixedNoRebuild {
		state = searchMixedRebuildFinished
	}
	switched := state == string(searchdomain.RebuildRetiring) || state == searchMixedRebuildFinished
	if switched && rebuild.Switched == 0 {
		rebuild.Switched = sample.Offset - rebuild.Started
	}
	rebuild.FinalState = state
	return rebuild
}

func (m *searchMixed) rebuildState(ctx context.Context) (string, error) {
	rebuild, found, err := m.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(ctx)
	if err != nil {
		return "", loggedError(ctx, "qa datagen search-mixed: the command could not read the index rebuild record", err)
	}
	if !found {
		return searchMixedNoRebuild, nil
	}
	return string(rebuild.State), nil
}

// catchUp waits for workers to finish all live, access, and cleanup work with an enqueue time
// at or before writesEnded. It stops waiting when the catch-up timeout expires.
func (m *searchMixed) catchUp(ctx context.Context, writesEnded time.Time) {
	deadline := clock.Now().Add(m.options.CatchUp)
	completed := false
	for {
		oldest, err := m.backlog.oldest(ctx)
		completed = err == nil && caughtUp(oldest, writesEnded)
		if completed || clock.Now().After(deadline) || !waitForSearchMixedSample(ctx) {
			break
		}
	}
	elapsed := clock.Now().Sub(writesEnded)
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.catchUp = SearchMixedCatchUp{Completed: completed, Elapsed: elapsed}
	})
	slog.InfoContext(ctx, "qa.datagen.search_mixed_catch_up_finished", slog.Bool("completed", completed),
		slog.Duration("elapsed", elapsed))
}

func caughtUp(oldest map[searchdomain.WorkClass]time.Time, writesEnded time.Time) bool {
	for _, class := range searchMixedCatchUpClasses() {
		if enqueued, pending := oldest[class]; pending && !enqueued.After(writesEnded) {
			return false
		}
	}
	return true
}

func waitForSearchMixedSample(ctx context.Context) bool {
	timer := time.NewTimer(searchMixedSampleInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
