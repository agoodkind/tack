// Each scratch yugabyted poll gives every probe its own deadline. A probe
// that does not answer cannot prevent the wait from checking for a stall.

package ops

import (
	"context"
	"time"
)

// ybScratchReading records one poll. failure explains a terminal step failure,
// and blind reports that the poll could not read the failure check. readErr
// records the last failed read in the poll. counters stays nil when the poll
// does not obtain a progress reading.
type ybScratchReading struct {
	exited   bool
	ready    bool
	failure  string
	blind    bool
	counters map[string]int64
	readErr  string
}

// readYBScratch collects one poll. A stopped container or terminal step failure
// ends the reading before the readiness check. A successful readiness check
// ends the reading before the progress check.
func readYBScratch(
	ctx context.Context,
	watch ybScratchWatch,
	timeout time.Duration,
) ybScratchReading {
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
		reading.blind = true
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
