// Each scratch yugabyted probe uses its own deadline. An unresponsive probe
// cannot prevent the wait loop from checking for stalled progress.

package ops

import (
	"context"
	"time"
)

// ybScratchReading records one poll's observations. failure records a permanent
// failure. blind indicates that the failure check failed. readErr records the
// last probe error. counters is nil when the poll did not read progress.
type ybScratchReading struct {
	exited   bool
	ready    bool
	failure  string
	blind    bool
	counters map[string]int64
	readErr  string
}

// readYBScratch checks container state, failure, readiness, and progress in order.
// Container exit and permanent failure end the poll before the readiness check.
// A successful readiness check ends the poll before the progress check.
func readYBScratch(
	ctx context.Context,
	watch ybScratchWatch,
	timeout time.Duration,
) ybScratchReading {
	var progressReading ybScratchReading
	containerRunning, err := probeYBScratch(ctx, timeout, watch.Running)
	switch {
	case err != nil:
		progressReading.readErr = "container inspect: " + err.Error()
	case !containerRunning:
		progressReading.exited = true
		return progressReading
	}

	failure, err := probeYBScratch(ctx, timeout, watch.Failed)
	switch {
	case err != nil:
		progressReading.readErr = "failure check: " + err.Error()
		progressReading.blind = true
	case failure != "":
		progressReading.failure = failure
		return progressReading
	}

	ready, err := probeYBScratch(ctx, timeout, watch.Ready)
	if err == nil && ready {
		progressReading.ready = true
		return progressReading
	}

	if err != nil {
		progressReading.readErr = "readiness check: " + err.Error()
	}

	counters, err := probeYBScratch(ctx, timeout, watch.Progress)
	if err != nil {
		progressReading.readErr = "progress read: " + err.Error()
		return progressReading
	}

	progressReading.counters = counters

	return progressReading
}

// probeYBScratchResult restricts the values a scratch probe can return.
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
