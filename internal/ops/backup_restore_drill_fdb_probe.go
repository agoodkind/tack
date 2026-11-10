// Each FoundationDB restore poll uses separate deadlines for its exit and
// status probes. The watch compares successive readings to detect inactivity.
// Its stall check runs only between readings. Before probe deadlines, a hung
// container exec could block that check and leave the drill waiting on the
// exec indefinitely.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// fdbDrillProbeTimeout limits each exit or status probe. Each reads a small
	// file or the restore's status keys. The original deadline rationale treats
	// that work as constant regardless of restore size and expects healthy probes
	// to finish within the deadline even for a large restore. Restore work itself
	// scales with the dataset and uses an inactivity limit.
	fdbDrillProbeTimeout = 2 * time.Minute

	// fdbDrillProbeTimeoutTolerance limits consecutive polls with an unanswered
	// probe. A timeout does not establish progress or a stalled restore; it
	// establishes that the drill could not observe the restore.
	//
	// The original tolerance rationale allows one transient timeout for a Docker
	// daemon under load or scratch-cluster recovery. It expects either condition
	// to clear within a probe deadline. Three consecutive timeouts represent six
	// minutes without executing even a shell in the scratch container, which
	// the original rationale treats as incompatible with a healthy restore or
	// host. The drill cannot verify a restore it cannot observe.
	//
	// The drill stops before the stall window and reports the unanswered probes
	// instead of claiming that it observed a stall.
	fdbDrillProbeTimeoutTolerance = 3
)

// errFDBRestoreProbeTimedOut distinguishes an unanswered probe from a probe
// that returns an error before its deadline.
var errFDBRestoreProbeTimedOut = errors.New("the probe did not answer")

// fdbRestoreReading records one poll's exit code and completion state, status
// text, or probe failure, including an unanswered probe.
type fdbRestoreReading struct {
	exitCode int
	done     bool
	status   string
	// statusErr records why the reading lacks status: a failed or unanswered
	// probe.
	statusErr error
	// timedOut reports whether a probe exceeded its deadline during this poll.
	timedOut bool
}

// readFDBRestore collects one poll's reading. After an exit-probe timeout,
// it skips the status probe. Both execs use the same access to the scratch
// container, and a second probe would double the time without observation.
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

// runFDBRestoreProbe executes one probe with its own deadline and returns
// its answer or a timeout reading. A separate goroutine permits the wait to
// end at the deadline regardless of probe behavior. Cancellation ends the
// underlying exec; the caller does not consume a late answer.
//
// A probe that returns its own deadline error counts as unanswered. Its answer
// and the context's done channel can become ready together. Either branch of
// the select must produce the same timeout classification.
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
			err := fmt.Errorf("the probe was cancelled: %w", ctx.Err())

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

// classifyFDBRestoreProbeAnswer checks whether a returned answer represents
// the probe's own deadline. The answer must include a deadline error, the
// probe context must have expired with that error, and the drill context
// must remain active. A nested operation's deadline error while the probe
// context remains active counts as an answered failure.
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

// fdbRestoreBlindError reports consecutive polls with unanswered probes,
// the greatest observed progress, and the last probe failure when the drill
// can no longer observe the restore.
func fdbRestoreBlindError(
	polls int,
	probeTimeout time.Duration,
	progress *fdbRestoreProgress,
	lastErr string,
) error {
	return fmt.Errorf(
		"the FoundationDB restore could not be observed: %d consecutive polls had a probe"+
			" that did not answer within %s, so whether it is still moving is unknown; the furthest it was"+
			" seen: %s; the last probe failed: %s",
		polls,
		probeTimeout,
		progress.summary(),
		lastErr,
	)
}
