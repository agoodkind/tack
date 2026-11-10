package datagen

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

// SoakOptions controls one continuous soak run.
type SoakOptions struct {
	Duration time.Duration
	Rate     int
	MaxOps   int
	Scale    string
	Seed     int64
	Commit   bool
}

// SoakSummary reports completed soak operations.
type SoakSummary struct {
	Seed          int64
	DryRun        bool
	StopReason    string
	Operations    int
	Created       int
	Updated       int
	Relationships int
	Comments      int
	Reads         int
	// Searches counts answered tack_search calls. SearchesUnavailable counts
	// unavailable responses, including refusals when public search is disabled.
	Searches            int
	SearchesUnavailable int
	QuietOps            int
	SpikeOps            int
	Elapsed             time.Duration
	// Latency reports the completed operation count and p50 and p95 wall time
	// for each operation kind.
	Latency []SoakOperationLatency
}

// Soak schedules sustained MCP traffic against a prepared corpus.
type Soak struct {
	driver    *Driver
	content   *Content
	projects  []*soakProject
	options   SoakOptions
	summary   SoakSummary
	clock     clock.Clock
	latencies soakLatencies
}

// RunSoak prepares a corpus and schedules operations until a configured stop
// condition ends the run.
//
// Setup uses discovered server projects. If discovery does not find a usable
// project, setup seeds the corpus and repeats discovery. Setup fails if the
// second discovery still does not find a usable project.
//
//   - Option validation precedes identity bootstrap.
//   - Project preparation precedes content generation.
func RunSoak(
	scheduleContext context.Context,
	callContext context.Context,
	cfg *config.Config,
	options SoakOptions,
	stop <-chan struct{},
) (SoakSummary, error) {
	if err := validateSoakOptions(options); err != nil {
		return SoakSummary{}, err
	}
	if err := soakContextError(scheduleContext, "start setup"); err != nil {
		return SoakSummary{}, err
	}
	session, err := openRunSession(
		callContext, cfg, options.Scale, options.Seed, options.Commit,
	)
	if err != nil {
		return SoakSummary{}, err
	}
	defer session.close()
	if err := soakContextError(scheduleContext, "finish identity bootstrap"); err != nil {
		return SoakSummary{}, err
	}
	projects, err := prepareSoakProjects(scheduleContext, callContext, session)
	if err != nil {
		return SoakSummary{}, err
	}
	content, err := NewContent(callContext, options.Seed^0x2455A0)
	if err != nil {
		return SoakSummary{}, err
	}
	if err := soakContextError(scheduleContext, "finish soak content setup"); err != nil {
		return SoakSummary{}, err
	}
	soak := &Soak{
		driver: session.driver, content: content, projects: projects, options: options,
		summary: SoakSummary{Seed: options.Seed, DryRun: !options.Commit},
		clock:   clock.Wall{},
	}
	return soak.Run(scheduleContext, callContext, stop)
}

func prepareSoakProjects(
	scheduleContext context.Context,
	callContext context.Context,
	session *runSession,
) ([]*soakProject, error) {
	projects, _, err := discoverSoakProjects(callContext, session)
	if err != nil {
		return nil, err
	}
	if err := soakContextError(scheduleContext, "finish project discovery"); err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		if _, err := session.seedCorpus(callContext, false); err != nil {
			return nil, loggedError(callContext, "qa datagen soak: seed corpus", err)
		}
		if err := soakContextError(scheduleContext, "finish fallback seed"); err != nil {
			return nil, err
		}
		projects, _, err = discoverSoakProjects(callContext, session)
		if err != nil {
			return nil, err
		}
		if err := soakContextError(scheduleContext, "finish seeded project discovery"); err != nil {
			return nil, err
		}
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("qa datagen soak: no usable projects after fallback seed")
	}
	return projects, nil
}

func soakContextError(ctx context.Context, step string) error {
	if err := ctx.Err(); err != nil {
		return loggedError(ctx, "qa datagen soak: "+step, err)
	}
	return nil
}

// Run executes scheduled operations and waits for calls already in progress
// to finish before returning.
//
// Each successful operation increases the completed operation count and the
// count for its quiet or spike phase.
//
//   - A stop condition ends scheduling before the next operation starts.
//   - An operation error returns the summary with the error.
func (s *Soak) Run(
	scheduleContext context.Context,
	callContext context.Context,
	stop <-chan struct{},
) (SoakSummary, error) {
	startedAt := s.clock.Now()
	lastStartedAt := startedAt
	for {
		reason := s.stopReason(scheduleContext, stop, startedAt)
		if reason != "" {
			return s.stopResult(scheduleContext, startedAt, reason)
		}
		elapsed := s.clock.Since(startedAt)
		phase := phaseAt(elapsed, defaultBurstWindow)
		delay := phaseInterval(s.options.Rate, phase) - s.clock.Since(lastStartedAt)
		if delay < 0 {
			delay = 0
		}
		if s.options.Duration > 0 {
			remaining := s.options.Duration - elapsed
			if remaining < delay {
				delay = remaining
			}
		}
		proceed, err := waitForSoakOperation(
			scheduleContext, stop, delay,
		)
		if err != nil {
			return s.finish(scheduleContext, startedAt, "context"), err
		}
		if !proceed {
			return s.finish(scheduleContext, startedAt, "signal"), nil
		}
		if reason := s.stopReason(scheduleContext, stop, startedAt); reason != "" {
			return s.stopResult(scheduleContext, startedAt, reason)
		}
		lastStartedAt = s.clock.Now()
		if err := s.executeOperation(callContext, s.summary.Operations); err != nil {
			return s.finish(scheduleContext, startedAt, "error"), err
		}
		s.summary.Operations++
		if phase == burstSpike {
			s.summary.SpikeOps++
		} else {
			s.summary.QuietOps++
		}
	}
}
