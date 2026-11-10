package datagen

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchMixedBurstDivisor schedules the ingestion burst at one quarter of the
// measured window.
const searchMixedBurstDivisor = 4

// The summary lists these phase identifiers in SkippedPhases.
const (
	searchMixedPhaseSessions        = "sessions"
	searchMixedPhaseBurst           = "burst"
	searchMixedPhasePendingDeletion = "pending_deletion"
	searchMixedPhaseRevocation      = "revocation"
	searchMixedPhaseRebuild         = "rebuild"
	searchMixedPhaseRebuildSessions = "rebuild_sessions"
)

// errSearchMixedPhaseNotStarted reports that the schedule context ended while
// the burst phase waited for its start time.
var errSearchMixedPhaseNotStarted = errors.New(
	"qa datagen search-mixed: the schedule context ended before the phase started",
)

// searchMixedPhase is one workload phase and its identifier.
type searchMixedPhase struct {
	name string
	run  func() error
}

// runPhases checks read sessions, waits for the ingestion burst start, and
// runs the burst. It then checks whether searches return deleted issues and
// whether membership revocation prevents access after cached memberships
// expire. A requested rebuild starts next, followed by another read session
// check.
//
// Every phase uses callContext. The burst waits for its start inside
// phase.run. If scheduleContext ends during that wait, phase.run returns
// errSearchMixedPhaseNotStarted. runPhases records the burst and remaining
// phases as skipped and returns nil.
//
// If scheduleContext ends after a phase starts, runPhases waits for that phase
// to return. Other phase errors return before the next schedule check. When
// that check finds that scheduleContext has ended, runPhases records every
// unstarted phase as skipped and returns nil.
func (m *searchMixed) runPhases(
	scheduleContext context.Context,
	callContext context.Context,
	windowStarted time.Time,
) error {
	burstStart := windowStarted.Add(m.options.Duration / searchMixedBurstDivisor)
	sessions := func() error {
		m.checkSessions(callContext)
		return nil
	}
	phases := []searchMixedPhase{
		{name: searchMixedPhaseSessions, run: sessions},
		{
			name: searchMixedPhaseBurst,
			run: func() error {
				if !waitForSearchLoadStart(scheduleContext, burstStart) {
					return errSearchMixedPhaseNotStarted
				}
				m.burst(callContext, windowStarted)
				return nil
			},
		},
		{
			name: searchMixedPhasePendingDeletion,
			run:  func() error { return m.checkPendingDeletion(callContext) },
		},
		{
			name: searchMixedPhaseRevocation,
			run:  func() error { return m.checkRevocation(callContext) },
		},
	}
	if m.options.Rebuild != nil {
		phases = append(
			phases,
			searchMixedPhase{
				name: searchMixedPhaseRebuild,
				run:  func() error { return m.startRebuild(callContext, windowStarted) },
			},
			searchMixedPhase{name: searchMixedPhaseRebuildSessions, run: sessions},
		)
	}
	for position, phase := range phases {
		if scheduleContext.Err() != nil {
			m.skipPhases(callContext, phases[position:])
			return nil
		}
		err := phase.run()
		if errors.Is(err, errSearchMixedPhaseNotStarted) {
			m.skipPhases(callContext, phases[position:])
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// searchMixedPhaseNames returns the identifiers for the phases that runPhases
// runs, in order.
func searchMixedPhaseNames(rebuild bool) []string {
	names := []string{
		searchMixedPhaseSessions,
		searchMixedPhaseBurst,
		searchMixedPhasePendingDeletion,
		searchMixedPhaseRevocation,
	}
	if rebuild {
		names = append(names, searchMixedPhaseRebuild, searchMixedPhaseRebuildSessions)
	}
	return names
}

// skipPhases records identifiers for phases that a stop prevented from
// starting.
func (m *searchMixed) skipPhases(ctx context.Context, phases []searchMixedPhase) {
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, phase.name)
	}
	m.phases.update(func(stats *searchMixedPhaseStats) { stats.skipped = names })
	slog.InfoContext(
		ctx,
		"qa.datagen.search_mixed_phases_skipped",
		slog.String("phases", strings.Join(names, ",")),
	)
}

func (m *searchMixed) startRebuild(ctx context.Context, windowStarted time.Time) error {
	target, err := m.options.Rebuild(ctx)
	if err != nil {
		return loggedError(
			ctx,
			"qa datagen search-mixed: the command could not start the index rebuild",
			err,
		)
	}
	started := clock.Now()
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.rebuild = SearchMixedRebuildResult{
			Requested:   true,
			TargetIndex: target,
			Started:     started.Sub(windowStarted),
			Switched:    0,
			FinalState:  string(searchdomain.RebuildCreating),
		}
	})
	return nil
}
