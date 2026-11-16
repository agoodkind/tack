// FoundationDB restore monitoring limits inactivity, not total work. Restore
// duration grows with the dataset; increasing work counters permit continued
// execution without an overall restore deadline.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// fdbDrillRestoreStallWindow tolerates quiet intervals during backup-file
// enumeration and between final block completion and process exit.
const fdbDrillRestoreStallWindow = 30 * time.Minute

// fdbDrillRestorePollInterval sets progress counter sampling frequency.
const fdbDrillRestorePollInterval = 15 * time.Second

// fdbRestoreWatch supplies exit and status probes for each monitoring poll.
// Each probe runs under a separate deadline and must respect cancellation.
type fdbRestoreWatch struct {
	// Finished reports the launched --waitfordone process's completed exit
	// status or done=false while it remains active.
	Finished func(ctx context.Context) (exitCode int, done bool, err error)
	// Status returns one fdbrestore status reading.
	Status func(ctx context.Context) (string, error)
}

// awaitFDBRestore waits for exit or inactivity without limiting total work.
// Probe deadlines permit stall checks despite an unanswered exec. Consecutive
// probe timeouts fail observation after fdbDrillProbeTimeoutTolerance polls
// rather than falsely reporting a restore stall that monitoring did not
// observe.
func awaitFDBRestore(
	ctx context.Context,
	watch fdbRestoreWatch,
	stallWindow time.Duration,
	pollInterval time.Duration,
	probeTimeout time.Duration,
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
				return progress, 0, fdbRestoreBlindError(
					blindPolls,
					probeTimeout,
					progress,
					lastStatusErr,
				)
			}
		case reading.statusErr != nil:
			blindPolls = 0
			lastStatusErr = reading.statusErr.Error()
		case progress.observe(reading.status):
			blindPolls = 0
			lastMoved = opsNow()
			lastStatusErr = ""
		default:
			// A readable unchanged status clears observation errors without
			// indicating new work or resetting inactivity.
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

// fdbRestoreWaitCancelled reports parent cancellation and recorded progress.
func fdbRestoreWaitCancelled(ctx context.Context, progress *fdbRestoreProgress) error {
	wrapped := fmt.Errorf("waiting for the FoundationDB restore: %w", ctx.Err())
	telemetry.L(ctx).ErrorContext(
		ctx,
		"backup.restore_drill.fdb.wait_cancelled",
		slog.String("err", wrapped.Error()),
		slog.String("progress", progress.summary()),
	)

	return wrapped
}

// fdbRestoreStallError reports inactivity, counter extremes, and the last
// status read failure to distinguish stopped work from unreadable status.
func fdbRestoreStallError(
	stalled time.Duration,
	progress *fdbRestoreProgress,
	lastStatusErr string,
) error {
	message := fmt.Sprintf(
		"the FoundationDB restore did not make progress for %s: %s",
		stalled.Round(time.Second),
		progress.summary(),
	)
	if lastStatusErr != "" {
		message += "; the last status read failed: " + lastStatusErr
	}

	return errors.New(message)
}
