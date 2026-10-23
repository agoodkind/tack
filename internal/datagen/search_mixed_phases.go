package datagen

import (
	"context"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchMixedBurstDivisor places the ingestion burst at one quarter of the measured window.
const searchMixedBurstDivisor = 4

// runPhases checks read sessions, waits for the ingestion burst start, and runs the burst.
// It then checks whether searches return deleted issues and whether membership revocation
// prevents access after cached memberships expire. If the run requests a rebuild, runPhases
// starts it and checks the read sessions again.
func (m *searchMixed) runPhases(scheduleContext, callContext context.Context, windowStarted time.Time) error {
	m.checkSessions(callContext)
	if !waitForSearchLoadStart(scheduleContext, windowStarted.Add(m.options.Duration/searchMixedBurstDivisor)) {
		return nil
	}
	m.burst(callContext, windowStarted)
	if err := m.checkPendingDeletion(callContext); err != nil {
		return err
	}
	if err := m.checkRevocation(callContext); err != nil {
		return err
	}
	if m.options.Rebuild == nil {
		return nil
	}
	if err := m.startRebuild(callContext, windowStarted); err != nil {
		return err
	}
	m.checkSessions(callContext)
	return nil
}

func (m *searchMixed) startRebuild(ctx context.Context, windowStarted time.Time) error {
	target, err := m.options.Rebuild(ctx)
	if err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not start the index rebuild", err)
	}
	started := clock.Now()
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.rebuild = SearchMixedRebuildResult{
			Requested: true, TargetIndex: target, Started: started.Sub(windowStarted), Switched: 0,
			FinalState: string(searchdomain.RebuildCreating),
		}
	})
	return nil
}
