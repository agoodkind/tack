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
	// ybScratchProbeTimeout bounds one probe. Most probes do constant work (an
	// inspect, one readiness command, one status page); the data directory walk
	// grows with the scratch engine's file count, which at today's ledger size
	// takes well under a second. A probe that hits the deadline counts as a
	// failed read, not as progress.
	ybScratchProbeTimeout = 2 * time.Minute
)

// ybScratchWatch is what the wait reads on each poll. All three are functions
// so the loop's decisions are exercised in tests without a Docker daemon.
type ybScratchWatch struct {
	// Running reports whether the scratch container is still running.
	Running func(ctx context.Context) (bool, error)
	// Failed returns a reason the step can never succeed, such as a crashed
	// process yugabyted restarted or a restoration the master marked failed,
	// or "" while the step can still succeed. Without it, the bytes a
	// restarting engine writes would read as progress.
	Failed func(ctx context.Context) (string, error)
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
// container stops running, the failure check reports a reason, or nothing
// moves for stallWindow. step names what
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
		if reading.failure != "" {
			err := fmt.Errorf("%s: %s: %s", step, reading.failure, progress.summary())
			slog.ErrorContext(ctx, "backup.restore_drill.yb.wait_failed", slog.String("err", err.Error()))
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
