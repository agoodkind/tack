// backup_restore_drill_yb_wait.go decides when the drill's scratch yugabyted
// has stopped moving, not when it has taken too long. Starting the scratch
// engine and applying the restored snapshot both run on whatever CPU the owner
// guest has left, and the restoration grows with the ledger, so a fixed budget
// for either is a wall that a loaded host or a larger ledger hits while the
// work is still progressing (TACK-471). Only inactivity is bounded here, the
// way the FoundationDB restore wait and the ledger node wait bound it.

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
	// ybScratchStallWindow is how long the scratch yugabyted may show no
	// progress at all before the drill calls it stalled. It bounds inactivity
	// and never total work, so it does not grow with the ledger.
	ybScratchStallWindow = 10 * time.Minute
	// ybScratchPollInterval is how often the drill reads the scratch engine.
	ybScratchPollInterval = 10 * time.Second
	// ybScratchProbeTimeout bounds one reading. Each probe does constant work
	// (an inspect, one readiness command, one status page), so a large ledger
	// never makes a healthy probe approach it; a reading that hits it is
	// counted as a failed read, not as progress.
	ybScratchProbeTimeout = 2 * time.Minute
)

// ybScratchWatch is what the wait reads on each poll. All three are functions
// so the loop's decisions are exercised in tests without a Docker daemon.
type ybScratchWatch struct {
	// Running reports whether the scratch container is still running.
	Running func(ctx context.Context) (bool, error)
	// Ready reports whether the step's readiness check passed.
	Ready func(ctx context.Context) (bool, error)
	// Progress returns counters that only rise while the engine does work.
	Progress func(ctx context.Context) (map[string]int64, error)
}

// ybScratchProgress remembers the highest value each counter has reached.
// Marks, rather than the last reading, stop a count that moves both ways from
// looking like progress forever.
type ybScratchProgress struct {
	highWater map[string]int64
}

func newYBScratchProgress() *ybScratchProgress {
	return &ybScratchProgress{highWater: map[string]int64{}}
}

// observe records one reading and reports whether any counter rose past its
// mark. A counter seen for the first time counts as movement, because a status
// page that starts answering is progress for an engine that is starting.
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

// summary renders every mark in a stable order, and says so when no counter
// was ever read.
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

// awaitYBScratch blocks until the step's readiness check passes, the scratch
// container stops running, or nothing moves for stallWindow. step names what
// is being waited for in every error. A read that fails is remembered and
// carried into the stall error, so a wedged engine reads differently from one
// the drill could not look at.
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
	for {
		reading := readYBScratch(ctx, watch, probeTimeout)
		if reading.exited {
			err := fmt.Errorf("%s: the scratch yugabyted container is not running: %s", step, progress.summary())
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_exited", slog.String("err", err.Error()))
			return 0, err
		}
		if reading.ready {
			return opsNow().Sub(started), nil
		}
		if reading.readErr != "" {
			lastReadErr = reading.readErr
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

// ybScratchReading is what one poll observed. readErr is the last read that
// failed this poll, and counters is nil when no progress was read.
type ybScratchReading struct {
	exited   bool
	ready    bool
	counters map[string]int64
	readErr  string
}

// readYBScratch takes one poll's reading, each probe under its own deadline.
// A container that is not running ends the reading before anything else is
// asked, and a passed readiness check ends it before progress is read.
func readYBScratch(ctx context.Context, watch ybScratchWatch, timeout time.Duration) ybScratchReading {
	var reading ybScratchReading
	runningCtx, cancelRunning := context.WithTimeout(ctx, timeout)
	running, err := watch.Running(runningCtx)
	cancelRunning()
	switch {
	case err != nil:
		reading.readErr = "container inspect: " + err.Error()
	case !running:
		reading.exited = true
		return reading
	}
	readyCtx, cancelReady := context.WithTimeout(ctx, timeout)
	ready, err := watch.Ready(readyCtx)
	cancelReady()
	if err == nil && ready {
		reading.ready = true
		return reading
	}
	if err != nil {
		reading.readErr = "readiness check: " + err.Error()
	}
	progressCtx, cancelProgress := context.WithTimeout(ctx, timeout)
	counters, err := watch.Progress(progressCtx)
	cancelProgress()
	if err != nil {
		reading.readErr = "progress read: " + err.Error()
		return reading
	}
	reading.counters = counters
	return reading
}

// ybScratchStallError names what the drill saw before it gave up: how long
// nothing moved, the furthest mark of every counter, and the last failed read.
func ybScratchStallError(step string, stalled time.Duration, progress *ybScratchProgress, lastReadErr string) error {
	message := fmt.Sprintf("%s: the scratch yugabyted made no progress for %s: %s",
		step, stalled.Round(time.Second), progress.summary())
	if lastReadErr != "" {
		message += "; the last failed read: " + lastReadErr
	}
	return errors.New(message)
}
