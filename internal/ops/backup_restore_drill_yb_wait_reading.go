// backup_restore_drill_yb_wait_reading.go takes one poll's reading of the
// scratch yugabyted for the wait, with every probe under its own deadline, so
// a probe that never answers cannot keep the stall check from running.

package ops

import (
	"context"
	"time"
)

// ybScratchReading is what one poll observed. failure is a reason the step can
// never succeed, readErr is the last read that failed this poll, and counters
// is nil when no progress was read.
type ybScratchReading struct {
	exited   bool
	ready    bool
	failure  string
	counters map[string]int64
	readErr  string
}

// readYBScratch takes one poll's reading. A container that is not running, or
// a step that has failed for good, ends the reading before readiness is asked,
// and a passed readiness check ends it before progress is read.
func readYBScratch(ctx context.Context, watch ybScratchWatch, timeout time.Duration) ybScratchReading {
	var reading ybScratchReading
	running, err := probeYBScratch(ctx, timeout, watch.Running)
	switch {
	case err != nil:
		reading.readErr = "container inspect: " + err.Error()
	case !running:
		reading.exited = true
		return reading
	}
	failure, err := probeYBScratch(ctx, timeout, watch.Failed)
	switch {
	case err != nil:
		reading.readErr = "failure check: " + err.Error()
	case failure != "":
		reading.failure = failure
		return reading
	}
	ready, err := probeYBScratch(ctx, timeout, watch.Ready)
	if err == nil && ready {
		reading.ready = true
		return reading
	}
	if err != nil {
		reading.readErr = "readiness check: " + err.Error()
	}
	counters, err := probeYBScratch(ctx, timeout, watch.Progress)
	if err != nil {
		reading.readErr = "progress read: " + err.Error()
		return reading
	}
	reading.counters = counters
	return reading
}

// probeYBScratchResult is the set of values a scratch probe returns.
type probeYBScratchResult interface {
	bool | string | map[string]int64
}

// probeYBScratch runs one probe under its own deadline.
func probeYBScratch[T probeYBScratchResult](
	ctx context.Context,
	timeout time.Duration,
	probe func(context.Context) (T, error),
) (T, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return probe(probeCtx)
}
