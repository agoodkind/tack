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
// Docker daemon.
type fdbRestoreWatch struct {
	// Finished reports the exit code of the `fdbrestore ... --waitfordone`
	// the drill launched, and done=false while it is still running.
	Finished func(ctx context.Context) (exitCode int, done bool, err error)
	// Status returns one `fdbrestore status` reading.
	Status func(ctx context.Context) (string, error)
}

// awaitFDBRestore blocks until the restore exits or stops making progress. It
// never bounds the restore's total run time: a restore whose counters keep
// climbing runs for as long as the dataset needs. stallWindow bounds only the
// time since the last observed movement, so what fails the drill is a restore
// that stopped, never a restore that is big.
func awaitFDBRestore(
	ctx context.Context,
	watch fdbRestoreWatch,
	stallWindow, pollInterval time.Duration,
) (*fdbRestoreProgress, int, error) {
	progress := newFDBRestoreProgress()
	lastMoved := opsNow()
	lastStatusErr := ""
	for {
		exitCode, done, err := watch.Finished(ctx)
		if err != nil {
			return progress, 0, err
		}
		if done {
			return progress, exitCode, nil
		}
		status, statusErr := watch.Status(ctx)
		switch {
		case statusErr != nil:
			lastStatusErr = statusErr.Error()
		case progress.observe(status):
			lastMoved = opsNow()
			lastStatusErr = ""
		}
		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			return progress, 0, fdbRestoreStallError(stalled, progress, lastStatusErr)
		}
		select {
		case <-ctx.Done():
			wrapped := fmt.Errorf("waiting for the FoundationDB restore: %w", ctx.Err())
			telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.fdb.wait_cancelled",
				slog.String("err", wrapped.Error()), slog.String("progress", progress.summary()))
			return progress, 0, wrapped
		case <-time.After(pollInterval):
		}
	}
}

// fdbRestoreStallError names what the drill saw before it gave up: how long
// nothing moved, the high-water mark of every counter it read, and the last
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
