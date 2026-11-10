// Each restore drill acquires an exclusive lock on its scratch directory.
// The kernel releases the lock when the process ends, including after an
// out-of-memory kill that prevents cleanup. The orphan sweep can acquire a
// stopped run's lock and distinguish its leftover resources from a live run's
// scratch data.

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

// drillSweepLockName identifies the file that serializes run claims and orphan
// sweeps under the scratch root. The lock coordinates those operations while
// a run acquires its directory lock.
const drillSweepLockName = ".sweep.lock"

// drillScratchRoot returns the directory that contains every run's scratch
// directory.
func drillScratchRoot(r *restoreDrillCtx) string {
	return filepath.Join(r.Cfg.BackupRoot, drillScratchDirName)
}

// claimDrillRun creates the scratch directory, acquires the sweep lock, locks
// the run directory for the process lifetime, and sweeps stopped runs. The
// sweep lock serializes the claim and sweep after directory creation. Sweep
// failures do not stop the drill. Scratch-removal and staging RemoveAll errors
// produce logs; container-removal and staging-path Stat errors do not.
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

// tryLockDrillRun attempts an exclusive run-directory lock without waiting.
// It returns the locked directory on success or live when another process
// owns the lock. A missing directory returns neither. A drill locks its
// directory before starting scratch engines.
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

// releaseDrillRun releases the run's lock. Cleanup invokes releaseDrillRun
// last, after attempting scratch-directory removal, even when that removal
// fails and leaves the directory behind.
func releaseDrillRun(r *restoreDrillCtx) {
	if r.runLock != nil {
		_ = r.runLock.Close()
		r.runLock = nil
	}
}
