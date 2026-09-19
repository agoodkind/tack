// backup_restore_drill_runlock.go marks a restore drill as running for as long
// as its process lives. Each drill holds an exclusive lock on its own scratch
// directory, and the kernel drops the lock when the process ends, however it
// ends, so a drill killed before its teardown (an out-of-memory kill, say)
// leaves resources whose lock any later drill can take. That is how the
// orphan sweep tells a dead run's leftovers from a live run's scratch.

package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// drillSweepLockName is the file under the scratch root that a drill holds
// exclusively while it claims its run and sweeps orphans, so no sweep sees
// a run whose directory exists but is not locked yet.
const drillSweepLockName = ".sweep.lock"

// drillScratchRoot is the directory holding every run's scratch directory.
func drillScratchRoot(r *restoreDrillCtx) string {
	return filepath.Join(r.Cfg.BackupRoot, drillScratchDirName)
}

// claimDrillRun creates this run's scratch directory, locks it for the rest of
// the process's life, and then removes what killed runs left behind. The claim
// and the sweep run under the sweep lock, so a drill starting beside this one
// either holds its own lock before this sweep looks or looks after this run is
// locked. A sweep failure is logged and does not stop the drill.
func claimDrillRun(ctx context.Context, r *restoreDrillCtx) error {
	root := drillScratchRoot(r)
	if err := os.MkdirAll(drillScratchRunDir(r), 0o755); err != nil {
		wrapped := fmt.Errorf("mkdir drill scratch %s: %w", drillScratchRunDir(r), err)
		slog.ErrorContext(ctx, "backup.restore_drill.claim_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	sweepLock, err := os.OpenFile(filepath.Join(root, drillSweepLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		wrapped := fmt.Errorf("open the drill sweep lock under %s: %w", root, err)
		slog.ErrorContext(ctx, "backup.restore_drill.claim_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer sweepLock.Close()
	if err := syscall.Flock(int(sweepLock.Fd()), syscall.LOCK_EX); err != nil {
		wrapped := fmt.Errorf("lock the drill sweep lock under %s: %w", root, err)
		slog.ErrorContext(ctx, "backup.restore_drill.claim_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	runLock, live, err := tryLockDrillRun(ctx, root, r.RunID)
	if err != nil || live {
		if err == nil {
			err = errors.New("another process holds its lock")
		}
		wrapped := fmt.Errorf("lock drill run %s: %w", r.RunID, err)
		slog.ErrorContext(ctx, "backup.restore_drill.claim_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	r.runLock = runLock
	sweepDrillOrphans(ctx, r)
	return nil
}

// tryLockDrillRun takes the exclusive lock on run's scratch directory without
// waiting. It returns the locked directory, or live when another process
// holds the lock. A run with no scratch directory returns neither: no drill
// can be running it, since a drill locks its directory before it starts
// anything.
func tryLockDrillRun(ctx context.Context, root, run string) (*os.File, bool, error) {
	dir, err := os.Open(filepath.Join(root, run))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		wrapped := fmt.Errorf("open drill run directory %s: %w", run, err)
		slog.ErrorContext(ctx, "backup.restore_drill.lock_failed", slog.String("err", wrapped.Error()))
		return nil, false, wrapped
	}
	err = syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		_ = dir.Close()
		return nil, true, nil
	}
	if err != nil {
		_ = dir.Close()
		wrapped := fmt.Errorf("lock drill run directory %s: %w", run, err)
		slog.ErrorContext(ctx, "backup.restore_drill.lock_failed", slog.String("err", wrapped.Error()))
		return nil, false, wrapped
	}
	return dir, false, nil
}

// releaseDrillRun drops this run's lock. Teardown calls it last, after the
// run's scratch directory is gone.
func releaseDrillRun(r *restoreDrillCtx) {
	if r.runLock != nil {
		_ = r.runLock.Close()
		r.runLock = nil
	}
}
