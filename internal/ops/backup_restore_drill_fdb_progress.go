// The drill limits FoundationDB restore inactivity instead of total work.
// Restore duration grows with the dataset. A fixed total budget would
// eventually fail a healthy large restore and keep the rehearsal staleness
// alarm active. A restore that continues making progress can run as long as
// the dataset requires.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// fdbDrillRestoreStallWindow limits time without observed progress rather
// than total work and does not grow with the dataset. The window allows
// quiet periods while the backup agent lists files in the object store and
// after the last block completes but before the process exits.
const fdbDrillRestoreStallWindow = 30 * time.Minute

// fdbDrillRestorePollInterval sets the interval between progress-counter
// readings.
const fdbDrillRestorePollInterval = 15 * time.Second

// fdbRestoreWatch supplies the functions that each poll reads. Tests can
// exercise wait decisions with real status text without a Docker daemon.
// Each function runs with its own deadline and must return when its context
// ends.
type fdbRestoreWatch struct {
	// Finished returns the exit code of the launched
	// fdbrestore ... --waitfordone command. It returns done=false while that
	// command still runs.
	Finished func(ctx context.Context) (exitCode int, done bool, err error)
	// Status returns one `fdbrestore status` reading.
	Status func(ctx context.Context) (string, error)
}

// awaitFDBRestore waits for completion or loss of progress without imposing
// a total restore deadline. An advance by any counter in its expected
// direction resets the inactivity clock. stallWindow measures time since
// the last observed advance rather than dataset size.
//
// probeTimeout bounds each reading and permits the stall check to run even
// when a probe does not answer. Timeouts do not establish progress. After
// fdbDrillProbeTimeoutTolerance consecutive unanswered polls, the wait reports
// those polls instead of claiming that it observed a restore stall.
func awaitFDBRestore(
	ctx context.Context,
	watch fdbRestoreWatch,
	stallWindow, pollInterval, probeTimeout time.Duration,
) (*fdbRestoreProgress, int, error) {
	progress := newFDBRestoreProgress()
	lastMoved := opsNow()
	var lastStatusErr string
	blindPolls := 0
	for {
		reading, err := readFDBRestore(ctx, watch, probeTimeout)
		if ctx.Err() != nil {
			return progress, 0, fdbRestoreWaitCancelled(ctx, progress)
		}
		if err != nil {
			return progress, 0, err
		}
		if reading.done {
			return progress, reading.exitCode, nil
		}
		switch {
		case reading.timedOut:
			blindPolls++
			lastStatusErr = reading.statusErr.Error()
			if blindPolls >= fdbDrillProbeTimeoutTolerance {
				return progress, 0, fdbRestoreBlindError(blindPolls, probeTimeout, progress, lastStatusErr)
			}
		case reading.statusErr != nil:
			blindPolls = 0
			lastStatusErr = reading.statusErr.Error()
		case progress.observe(reading.status):
			blindPolls = 0
			lastMoved = opsNow()
			lastStatusErr = ""
		default:
			// A successful status read without progress resets the unanswered-poll
			// count and clears the previous status error.
			blindPolls = 0
			lastStatusErr = ""
		}
		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			return progress, 0, fdbRestoreStallError(stalled, progress, lastStatusErr)
		}
		select {
		case <-ctx.Done():
			return progress, 0, fdbRestoreWaitCancelled(ctx, progress)
		case <-time.After(pollInterval):
		}
	}
}

// fdbRestoreWaitCancelled reports cancellation by the drill's context and
// logs the restore's observed progress.
func fdbRestoreWaitCancelled(ctx context.Context, progress *fdbRestoreProgress) error {
	wrapped := fmt.Errorf("waiting for the FoundationDB restore: %w", ctx.Err())
	telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.fdb.wait_cancelled",
		slog.String("err", wrapped.Error()), slog.String("progress", progress.summary()))
	return wrapped
}

// fdbRestoreStallError reports the inactivity duration, each counter's
// greatest observed progress, and the last failed status read. These details
// distinguish an observed restore stall from an unreadable status.
func fdbRestoreStallError(stalled time.Duration, progress *fdbRestoreProgress, lastStatusErr string) error {
	message := fmt.Sprintf("the FoundationDB restore made no progress for %s: %s",
		stalled.Round(time.Second), progress.summary())
	if lastStatusErr != "" {
		message += "; the last status read failed: " + lastStatusErr
	}
	return errors.New(message)
}
