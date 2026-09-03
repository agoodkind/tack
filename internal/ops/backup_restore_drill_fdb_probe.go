// backup_restore_drill_fdb_probe.go takes one reading of a FoundationDB
// restore for the watch, with each of the two probes under its own deadline.
// The watch decides whether the restore stopped by comparing readings over
// time, so a probe that never answers would keep it from deciding anything:
// the stall check runs only between readings, and a container exec can hang,
// which left the drill waiting on the exec rather than on the restore.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// fdbDrillProbeTimeout bounds one probe: the exec that reads whether the
	// restore exited, or the one that reads its status. Each does constant
	// work, reading one small file or the restore's status keys, so nothing
	// about a large restore makes a healthy probe approach this, unlike the
	// restore itself, whose duration scales with the dataset and is bounded
	// only by inactivity.
	fdbDrillProbeTimeout = 2 * time.Minute

	// fdbDrillProbeTimeoutTolerance is how many consecutive polls may end in a
	// probe that did not answer before the drill gives up. A probe that times
	// out is not progress, and it is not evidence the restore stopped either:
	// it says only that the drill could not look. One is a transient, such as
	// the Docker daemon answering late under load or the scratch cluster in a
	// recovery, either of which clears within a probe deadline. Three in a row
	// is six minutes in which the drill could not run so much as a shell in
	// its own scratch container, which no healthy restore or host explains,
	// and a restore the drill cannot see is not one it can vouch for. Giving up
	// then, rather than after the stall window, keeps the failure attributable:
	// the error names the probes that did not answer instead of reporting a
	// stall the drill never observed.
	fdbDrillProbeTimeoutTolerance = 3
)

// errFDBRestoreProbeTimedOut marks a probe that did not answer within its
// deadline, so the watch can tell it from a probe that answered with an error.
var errFDBRestoreProbeTimedOut = errors.New("the probe did not answer")

// fdbRestoreReading is what one poll of the restore observed: whether it
// exited and with what, or one status reading, or a probe that failed or did
// not answer.
type fdbRestoreReading struct {
	exitCode int
	done     bool
	status   string
	// statusErr is why the reading carries no status: the status probe
	// failed, or a probe did not answer.
	statusErr error
	// timedOut is whether a probe hit its deadline this poll.
	timedOut bool
}

// readFDBRestore takes one poll's reading. The status is not asked for after
// the exit probe timed out: both are execs on the same path into the scratch
// container, so asking again would only double the time spent blind.
func readFDBRestore(ctx context.Context, watch fdbRestoreWatch, probeTimeout time.Duration) (fdbRestoreReading, error) {
	finished := runFDBRestoreProbe(ctx, probeTimeout, func(probeCtx context.Context) fdbRestoreReading {
		exitCode, done, err := watch.Finished(probeCtx)
		return fdbRestoreReading{exitCode: exitCode, done: done, status: "", statusErr: err, timedOut: false}
	})
	if finished.timedOut {
		return finished, nil
	}
	if finished.statusErr != nil {
		return finished, finished.statusErr
	}
	if finished.done {
		return finished, nil
	}
	return runFDBRestoreProbe(ctx, probeTimeout, func(probeCtx context.Context) fdbRestoreReading {
		status, err := watch.Status(probeCtx)
		return fdbRestoreReading{exitCode: 0, done: false, status: status, statusErr: err, timedOut: false}
	}), nil
}

// runFDBRestoreProbe runs one probe under its own deadline and returns what it
// answered, or a reading that says it did not answer in time. The probe runs
// on its own goroutine so the deadline holds however the probe behaves; a
// probe abandoned at the deadline keeps a cancelled context, which is what
// ends the exec behind it, and its late answer goes nowhere.
//
// A probe that answers with the deadline's own error is a probe that did not
// answer. The exec behind it returns that error at the moment the probe's
// context ends, so the answer and the context's done channel are ready
// together and the select may take either; which one it takes must not decide
// whether the poll counts as blind.
func runFDBRestoreProbe(
	ctx context.Context,
	timeout time.Duration,
	probe func(context.Context) fdbRestoreReading,
) fdbRestoreReading {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answered := make(chan fdbRestoreReading, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("the probe panicked: %v", recovered)
				slog.ErrorContext(ctx, "backup.restore_drill.fdb.probe_panic", slog.String("err", err.Error()))
				answered <- fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: err, timedOut: false}
			}
		}()
		answered <- probe(probeCtx)
	}()
	select {
	case reading := <-answered:
		if ctx.Err() == nil && errors.Is(reading.statusErr, context.DeadlineExceeded) {
			err := fmt.Errorf("%w within %s: %w", errFDBRestoreProbeTimedOut, timeout, reading.statusErr)
			return fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: err, timedOut: true}
		}
		return reading
	case <-probeCtx.Done():
		if ctx.Err() != nil {
			err := fmt.Errorf("the probe was cancelled: %w", ctx.Err())
			return fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: err, timedOut: false}
		}
		err := fmt.Errorf("%w within %s", errFDBRestoreProbeTimedOut, timeout)
		return fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: err, timedOut: true}
	}
}

// fdbRestoreBlindError names a restore the drill could no longer observe: how
// many polls in a row had a probe that did not answer, the furthest the
// counters were seen before that, and the last probe's failure.
func fdbRestoreBlindError(polls int, probeTimeout time.Duration, progress *fdbRestoreProgress, lastErr string) error {
	return fmt.Errorf("the FoundationDB restore could not be observed: %d consecutive polls had a probe"+
		" that did not answer within %s, so whether it is still moving is unknown; the furthest it was"+
		" seen: %s; the last probe failed: %s", polls, probeTimeout, progress.summary(), lastErr)
}
