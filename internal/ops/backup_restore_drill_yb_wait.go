// Scratch monitoring limits inactivity rather than total restore work.
// Startup CPU availability and ledger size affect completion time, not the
// permitted interval without progress (TACK-471).

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
	// ybScratchStallWindow limits inactivity independently of ledger size.
	ybScratchStallWindow = 10 * time.Minute
	// ybScratchPollInterval sets the delay between scratch engine probes.
	ybScratchPollInterval = 10 * time.Second
	// ybScratchBlindTolerance limits consecutive unreadable failure checks.
	// Crash-loop writes can increase counters without successful work, and
	// monitoring cannot establish health without the failure check. One
	// unanswered poll permits transient daemon delays.
	ybScratchBlindTolerance = 3
	// ybScratchProbeTimeout limits each inspect, command, status read, or data
	// directory scan. Timeout counts as a failed read, not progress.
	ybScratchProbeTimeout = 2 * time.Minute
)

// ybScratchWatch supplies the probes for one scratch monitoring poll.
type ybScratchWatch struct {
	// Running reports whether the scratch container remains active.
	Running func(ctx context.Context) (bool, error)
	// Failed reports a permanent failure or an empty string. It detects
	// crash loops that increasing write counters alone cannot distinguish.
	Failed func(ctx context.Context) (string, error)
	// Ready reports successful step readiness.
	Ready func(ctx context.Context) (bool, error)
	// Progress returns counters that increase during successful work.
	Progress func(ctx context.Context) (map[string]int64, error)
}

// ybScratchProgress records counter maxima to avoid counting oscillation as
// repeated progress.
type ybScratchProgress struct {
	highWater map[string]int64
}

func newYBScratchProgress() *ybScratchProgress {
	return &ybScratchProgress{highWater: map[string]int64{}}
}

// observe reports a new counter maximum. The first readable counter
// establishes initial progress during startup.
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

// summary prints recorded maxima in a stable order or reports unreadable
// counters when monitoring never obtained any.
func (p *ybScratchProgress) summary() string {
	if len(p.highWater) == 0 {
		return "scratch yugabyted never reported readable progress counters"
	}

	parts := make([]string, 0, len(p.highWater))
	for _, name := range slices.Sorted(maps.Keys(p.highWater)) {
		parts = append(parts, name+"="+strconv.FormatInt(p.highWater[name], 10))
	}

	return strings.Join(parts, " ")
}

// awaitYBScratch waits for readiness, container exit, permanent failure, or
// inactivity. Errors identify the step and last failed read to distinguish
// stalled work from unsuccessful observation.
func awaitYBScratch(
	ctx context.Context,
	step string,
	watch ybScratchWatch,
	stallWindow time.Duration,
	pollInterval time.Duration,
	probeTimeout time.Duration,
) (time.Duration, error) {
	progress := newYBScratchProgress()
	started := opsNow()
	lastMoved := started
	var lastReadErr string
	blindPolls := 0

	for {
		reading := readYBScratch(ctx, watch, probeTimeout)
		if reading.exited {
			err := fmt.Errorf(
				"%s: the scratch yugabyted container is not running: %s",
				step,
				progress.summary(),
			)
			slog.ErrorContext(
				ctx,
				"backup.restore_drill.yb.wait_exited",
				slog.String("err", err.Error()),
			)

			return 0, err
		}

		if reading.failure != "" {
			err := fmt.Errorf("%s: %s: %s", step, reading.failure, progress.summary())
			slog.ErrorContext(
				ctx,
				"backup.restore_drill.yb.wait_failed",
				slog.String("err", err.Error()),
			)

			return 0, err
		}

		if reading.ready {
			return opsNow().Sub(started), nil
		}

		lastReadErr = reading.readErr
		if reading.blind {
			blindPolls++
			if blindPolls >= ybScratchBlindTolerance {
				err := fmt.Errorf(
					"%s: the drill could not check whether the scratch yugabyted failed on %d polls in a row: %s (%s)",
					step,
					blindPolls,
					lastReadErr,
					progress.summary(),
				)
				slog.ErrorContext(
					ctx,
					"backup.restore_drill.yb.wait_blind",
					slog.String("err", err.Error()),
				)

				return 0, err
			}
		} else {
			blindPolls = 0
		}

		if reading.counters != nil && progress.observe(reading.counters) {
			lastMoved = opsNow()
			slog.DebugContext(
				ctx,
				"backup.restore_drill.yb.wait_progress",
				slog.String("step", step),
				slog.String("marks", progress.summary()),
			)
		}

		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			err := ybScratchStallError(step, stalled, progress, lastReadErr)
			slog.ErrorContext(
				ctx,
				"backup.restore_drill.yb.wait_stalled",
				slog.String("err", err.Error()),
			)

			return 0, err
		}

		if !opsWait(ctx, pollInterval) {
			err := fmt.Errorf(
				"%s: waiting for the scratch yugabyted: %w (%s)",
				step,
				ctx.Err(),
				progress.summary(),
			)
			slog.ErrorContext(
				ctx,
				"backup.restore_drill.yb.wait_cancelled",
				slog.String("err", err.Error()),
			)

			return 0, err
		}
	}
}

// ybScratchStallError reports inactivity duration, counter maxima, and the last
// failed read.
func ybScratchStallError(
	step string,
	stalled time.Duration,
	progress *ybScratchProgress,
	lastReadErr string,
) error {
	message := fmt.Sprintf(
		"%s: the scratch yugabyted made no progress for %s: %s",
		step,
		stalled.Round(time.Second),
		progress.summary(),
	)
	if lastReadErr != "" {
		message += "; the last failed read: " + lastReadErr
	}

	return errors.New(message)
}
