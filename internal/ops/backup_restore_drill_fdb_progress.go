// backup_restore_drill_fdb_progress.go decides when a FoundationDB restore has
// stopped, not when it has taken too long. A restore's duration scales with the
// dataset, so a fixed budget for the whole restore is a wall a growing corpus
// eventually hits, and a drill that fails for being large pins the rehearsal
// staleness alarm on with nothing actually wrong. Only inactivity is bounded
// here: a restore that keeps moving runs for as long as it needs.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// fdbDrillRestoreStallWindow is how long the restore may show no progress at
// all before the drill calls it stalled. It bounds inactivity and never total
// work, so it does not grow with the dataset. The window is wide because a
// restore is legitimately quiet while the backup agent enumerates the backup's
// files over the object store, and quiet again between the last block and the
// process exiting.
const fdbDrillRestoreStallWindow = 30 * time.Minute

// fdbDrillRestorePollInterval is how often the drill reads the restore's
// progress counters.
const fdbDrillRestorePollInterval = 15 * time.Second

// fdbRestoreWatch is what the wait reads on each poll. Both are functions so
// the loop's decisions can be exercised against real status text without a
// Docker daemon. Each is called under its own deadline and must return once
// its context ends.
type fdbRestoreWatch struct {
	// Finished reports the exit code of the `fdbrestore ... --waitfordone`
	// the drill launched, and done=false while it is still running.
	Finished func(ctx context.Context) (exitCode int, done bool, err error)
	// Status returns one `fdbrestore status` reading.
	Status func(ctx context.Context) (string, error)
}

// awaitFDBRestore blocks until the restore exits or stops making progress. It
// never bounds the restore's total run time: a restore whose counters keep
// moving in the direction that counter moves under work runs for as long as the
// dataset needs. stallWindow bounds only the time since the last observed
// movement, so what fails the drill is a restore that stopped, never a restore
// that is big. probeTimeout bounds each reading, so a probe that never answers
// cannot keep the stall check from running; a reading that timed out is not
// progress, and the wait gives up once fdbDrillProbeTimeoutTolerance polls in
// a row ended that way, naming them rather than a stall it never observed.
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
			// A status that was read but showed no movement: the drill can
			// see the restore, so neither failure count carries forward.
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

// fdbRestoreWaitCancelled reports a wait the drill's own context ended, with
// how far the restore had got.
func fdbRestoreWaitCancelled(ctx context.Context, progress *fdbRestoreProgress) error {
	wrapped := fmt.Errorf("waiting for the FoundationDB restore: %w", ctx.Err())
	telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.fdb.wait_cancelled",
		slog.String("err", wrapped.Error()), slog.String("progress", progress.summary()))
	return wrapped
}

// fdbRestoreStallError names what the drill saw before it gave up: how long
// nothing moved, the furthest mark of every counter it read, and the last
// status read that failed, so a wedged restore reads differently from a
// status the drill could not read.
func fdbRestoreStallError(stalled time.Duration, progress *fdbRestoreProgress, lastStatusErr string) error {
	message := fmt.Sprintf("the FoundationDB restore made no progress for %s: %s",
		stalled.Round(time.Second), progress.summary())
	if lastStatusErr != "" {
		message += "; the last status read failed: " + lastStatusErr
	}
	return errors.New(message)
}
