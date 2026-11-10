// The drill limits inactivity while starting scratch yugabyted and applying
// a restored snapshot. Both tasks share the owner guest's available CPU, and
// restore work grows with the ledger. TACK-471 recorded the fixed-budget
// defect: host load or ledger growth could exhaust a total time limit while
// work continued. FoundationDB restore and ledger node waits also limit
// inactivity.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ybScratchStallWindow limits how long scratch yugabyted can run without
	// progress before the drill reports a stall. It limits inactivity rather
	// than total work and does not increase with ledger size.
	ybScratchStallWindow = 10 * time.Minute
	// ybScratchPollInterval is how often the drill reads the scratch engine.
	ybScratchPollInterval = 10 * time.Second
	// ybScratchBlindTolerance sets the consecutive unreadable failure-check
	// count that ends the wait. A crash loop can keep writing bytes and reset
	// the inactivity timer; the failure check must detect that condition before
	// the unit's backstop timeout.
	//
	// The wait tolerates an isolated unreadable poll, such as a daemon response
	// that arrives late under load.
	ybScratchBlindTolerance = 3
	// ybScratchProbeTimeout sets a deadline context for each probe. The helper
	// calls callbacks synchronously, and each callback must honor cancellation
	// to return at its deadline.
	//
	// Container inspection, a readiness command and a status page involve fixed
	// tasks. The data-directory walk grows with the scratch engine's file count.
	// The original observation put that walk well below one second at the ledger
	// size then in use.
	//
	// A probe that reports its deadline as an error counts as a failed read
	// rather than progress.
	ybScratchProbeTimeout = 2 * time.Minute
)

// ybScratchWatch supplies the four probe functions that the wait reads on
// each poll. Tests supply those functions to exercise loop decisions without
// a Docker daemon.
type ybScratchWatch struct {
	// Running reports whether the scratch container is still running.
	Running func(ctx context.Context) (bool, error)
	// Failed returns a permanent failure reason, such as a process that
	// yugabyted restarted after a crash or a restoration the master marked
	// failed. An empty string means the check has not reported a permanent
	// failure. Without this check, restart writes could count as progress.
	Failed func(ctx context.Context) (string, error)
	// Ready reports whether the step's readiness check passed.
	Ready func(ctx context.Context) (bool, error)
	// Progress returns work counters for comparison with their highest observed
	// values. Only a new maximum counts as further progress.
	Progress func(ctx context.Context) (map[string]int64, error)
}

// ybScratchProgress tracks each counter's highest value. A decrease followed
// by recovery to that value does not count as further progress or keep
// resetting the inactivity timer.
type ybScratchProgress struct {
	highWater map[string]int64
}

func newYBScratchProgress() *ybScratchProgress {
	return &ybScratchProgress{highWater: map[string]int64{}}
}

// observe records a reading and reports whether any counter exceeds its
// previous maximum. The first value for a counter also counts as progress;
// an answering status page provides startup evidence.
func (p *ybScratchProgress) observe(counters map[string]int64) bool {
	moved := false
	for name, value := range counters {
		previous, seen := p.highWater[name]
		if seen && value <= previous {
			continue
		}
		p.highWater[name] = value
		moved = true
	}
	return moved
}

// summary renders counter maxima in sorted order. It reports when the wait
// has never read a counter.
func (p *ybScratchProgress) summary() string {
	if len(p.highWater) == 0 {
		return "no progress counter was ever readable from the scratch yugabyted"
	}
	parts := make([]string, 0, len(p.highWater))
	for _, name := range slices.Sorted(maps.Keys(p.highWater)) {
		parts = append(parts, name+"="+strconv.FormatInt(p.highWater[name], 10))
	}
	return strings.Join(parts, " ")
}

// awaitYBScratch waits for readiness while checking container state,
// permanent failure and inactivity. It also stops after consecutive unreadable
// failure checks meet the tolerance or the context cancels. Every error
// identifies the step.
//
// The stall error includes the latest poll's last read error when present.
// A recovered read error does not explain a later stall. The counter summary
// distinguishes stopped progress from counters the drill could never read.
func awaitYBScratch(
	ctx context.Context,
	step string,
	watch ybScratchWatch,
	stallWindow, pollInterval, probeTimeout time.Duration,
) (time.Duration, error) {
	progress := newYBScratchProgress()
	started := opsNow()
	lastMoved := started
	var lastReadErr string
	blindPolls := 0
	for {
		reading := readYBScratch(ctx, watch, probeTimeout)
		if reading.exited {
			err := fmt.Errorf("%s: the scratch yugabyted container is not running: %s", step, progress.summary())
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_exited", slog.String("err", err.Error()))
			return 0, err
		}
		if reading.failure != "" {
			err := fmt.Errorf("%s: %s: %s", step, reading.failure, progress.summary())
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_failed", slog.String("err", err.Error()))
			return 0, err
		}
		if reading.ready {
			return opsNow().Sub(started), nil
		}
		lastReadErr = reading.readErr
		if reading.blind {
			blindPolls++
			if blindPolls >= ybScratchBlindTolerance {
				err := fmt.Errorf("%s: the drill could not check whether the scratch yugabyted failed on %d polls in a row: %s (%s)",
					step, blindPolls, lastReadErr, progress.summary())
				slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_blind", slog.String("err", err.Error()))
				return 0, err
			}
		} else {
			blindPolls = 0
		}
		if reading.counters != nil && progress.observe(reading.counters) {
			lastMoved = opsNow()
			slog.DebugContext(ctx, "backup.restore_drill.yb.wait_progress",
				slog.String("step", step), slog.String("marks", progress.summary()))
		}
		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			err := ybScratchStallError(step, stalled, progress, lastReadErr)
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_stalled", slog.String("err", err.Error()))
			return 0, err
		}
		if !opsWait(ctx, pollInterval) {
			err := fmt.Errorf("%s: waiting for the scratch yugabyted: %w (%s)", step, ctx.Err(), progress.summary())
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_cancelled", slog.String("err", err.Error()))
			return 0, err
		}
	}
}

// ybScratchStallError reports the inactivity duration and every counter's
// maximum. It includes the latest poll's last read error when present.
func ybScratchStallError(step string, stalled time.Duration, progress *ybScratchProgress, lastReadErr string) error {
	message := fmt.Sprintf("%s: the scratch yugabyted made no progress for %s: %s",
		step, stalled.Round(time.Second), progress.summary())
	if lastReadErr != "" {
		message += "; the last failed read: " + lastReadErr
	}
	return errors.New(message)
}
