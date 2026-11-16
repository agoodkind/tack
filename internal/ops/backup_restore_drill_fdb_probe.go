// FoundationDB monitoring bounds each exit and status probe independently.
// An unanswered exec must not prevent the inactivity check between readings.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// fdbDrillProbeTimeout bounds reading a small status file or restore keys,
	// not dataset-sized restore work. Restore execution uses inactivity limits.
	fdbDrillProbeTimeout = 2 * time.Minute

	// fdbDrillProbeTimeoutTolerance permits transient daemon or cluster recovery
	// delays. Repeated unanswered polls fail observation explicitly rather
	// than claiming an unobserved restore stall.
	fdbDrillProbeTimeoutTolerance = 3
)

// errFDBRestoreProbeTimedOut distinguishes unanswered probes from answered
// probes that returned an error.
var errFDBRestoreProbeTimedOut = errors.New("the probe did not answer")

// fdbRestoreReading records one exit result, status result, or probe failure.
type fdbRestoreReading struct {
	exitCode int
	done     bool
	status   string
	// statusErr records the failed status or unanswered probe.
	statusErr error
	// timedOut identifies a probe deadline during this poll.
	timedOut bool
}

// readFDBRestore skips status after exit-probe timeout because both operations
// depend on the same scratch exec access. A second request would only increase
// the time without a usable observation.
func readFDBRestore(
	ctx context.Context,
	watch fdbRestoreWatch,
	probeTimeout time.Duration,
) (fdbRestoreReading, error) {
	finished := runFDBRestoreProbe(
		ctx,
		probeTimeout,
		func(probeCtx context.Context) fdbRestoreReading {
			exitCode, done, err := watch.Finished(probeCtx)

			return fdbRestoreReading{
				exitCode:  exitCode,
				done:      done,
				status:    "",
				statusErr: err,
				timedOut:  false,
			}
		},
	)
	if finished.timedOut {
		return finished, nil
	}

	if finished.statusErr != nil {
		return finished, finished.statusErr
	}

	if finished.done {
		return finished, nil
	}

	return runFDBRestoreProbe(
		ctx,
		probeTimeout,
		func(probeCtx context.Context) fdbRestoreReading {
			status, err := watch.Status(probeCtx)

			return fdbRestoreReading{
				exitCode:  0,
				done:      false,
				status:    status,
				statusErr: err,
				timedOut:  false,
			}
		},
	), nil
}

// runFDBRestoreProbe isolates execution in a goroutine to enforce deadlines.
// Cancellation ends the underlying exec; buffered answers after timeout cannot
// block the probe. Own-deadline errors require identical classification whether
// select receives the answer or observes context completion first.
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
				slog.ErrorContext(
					ctx,
					"backup.restore_drill.fdb.probe_panic",
					slog.String("err", err.Error()),
				)
				answered <- fdbRestoreReading{
					exitCode:  0,
					done:      false,
					status:    "",
					statusErr: err,
					timedOut:  false,
				}
			}
		}()
		answered <- probe(probeCtx)
	}()
	select {
	case reading := <-answered:
		return classifyFDBRestoreProbeAnswer(reading, probeCtx, ctx, timeout)
	case <-probeCtx.Done():
		if ctx.Err() != nil {
			err := fmt.Errorf("the drill canceled the probe: %w", ctx.Err())

			return fdbRestoreReading{
				exitCode:  0,
				done:      false,
				status:    "",
				statusErr: err,
				timedOut:  false,
			}
		}

		err := fmt.Errorf("%w within %s", errFDBRestoreProbeTimedOut, timeout)

		return fdbRestoreReading{
			exitCode:  0,
			done:      false,
			status:    "",
			statusErr: err,
			timedOut:  true,
		}
	}
}

// classifyFDBRestoreProbeAnswer requires the probe's own expired deadline and
// matching error while the parent remains active. A nested operation's earlier
// deadline error remains an ordinary answered failure.
func classifyFDBRestoreProbeAnswer(
	reading fdbRestoreReading,
	probeCtx context.Context,
	ctx context.Context,
	timeout time.Duration,
) fdbRestoreReading {
	if ctx.Err() != nil || !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return reading
	}

	if !errors.Is(reading.statusErr, context.DeadlineExceeded) {
		return reading
	}

	err := fmt.Errorf("%w within %s: %w", errFDBRestoreProbeTimedOut, timeout, reading.statusErr)

	return fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: err, timedOut: true}
}

// fdbRestoreBlindError reports unanswered poll count, probe deadline, recorded
// counter extremes, and the final probe failure.
func fdbRestoreBlindError(
	polls int,
	probeTimeout time.Duration,
	progress *fdbRestoreProgress,
	lastErr string,
) error {
	return fdbRestoreObservationError{
		Polls:        polls,
		ProbeTimeout: probeTimeout,
		Progress:     progress.summary(),
		LastError:    lastErr,
	}
}

type fdbRestoreObservationError struct {
	Polls        int
	ProbeTimeout time.Duration
	Progress     string
	LastError    string
}

func (failure fdbRestoreObservationError) Error() string {
	return fmt.Sprintf(
		"the FoundationDB restore could not be observed: %d consecutive polls had a probe"+
			" that did not answer within %s; current progress is unknown; recorded counter extremes:"+
			" %s; the last probe failed: %s",
		failure.Polls,
		failure.ProbeTimeout,
		failure.Progress,
		failure.LastError,
	)
}
