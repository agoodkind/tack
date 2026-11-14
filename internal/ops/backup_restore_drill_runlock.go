// Each restore drill locks its scratch directory until the process exits.
// The kernel releases the lock even after a forced termination. The orphan
// sweep can lock abandoned directories but cannot lock active runs.

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

// drillSweepLockName identifies the shared lock for run claims and orphan
// sweeps. Serialization prevents a sweep from inspecting a new directory
// before its run acquires the directory lock.
const drillSweepLockName = ".sweep.lock"

// drillScratchRoot returns the parent directory for all scratch runs.
func drillScratchRoot(r *restoreDrillCtx) string {
	return filepath.Join(r.Cfg.BackupRoot, drillScratchDirName)
}

// claimDrillRun creates and locks the run directory, then sweeps abandoned
// resources. The shared sweep lock serializes claims and sweeps across
// processes. Sweep failures log an error but do not stop the drill.
func claimDrillRun(ctx context.Context, r *restoreDrillCtx) error {
	root := drillScratchRoot(r)
	if err := os.MkdirAll(drillScratchRunDir(r), 0o755); err != nil {
		wrapped := fmt.Errorf("mkdir drill scratch %s: %w", drillScratchRunDir(r), err)
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.claim_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	sweepLock, err := os.OpenFile(
		filepath.Join(root, drillSweepLockName),
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		wrapped := fmt.Errorf("open the drill sweep lock under %s: %w", root, err)
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.claim_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}
	defer sweepLock.Close()
	if err := syscall.Flock(int(sweepLock.Fd()), syscall.LOCK_EX); err != nil {
		wrapped := fmt.Errorf("lock the drill sweep lock under %s: %w", root, err)
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.claim_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	runLock, live, err := tryLockDrillRun(ctx, root, r.RunID)
	if err != nil || live {
		if err == nil {
			err = errors.New("another process owns its lock")
		}

		wrapped := fmt.Errorf("lock drill run %s: %w", r.RunID, err)
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.claim_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	r.runLock = runLock
	sweepDrillOrphans(ctx, r)

	return nil
}

// tryLockDrillRun attempts a directory lock without waiting. It returns the
// open directory on success or live when another process owns the lock.
// An absent directory returns neither because drills lock their directories
// before starting resources.
func tryLockDrillRun(ctx context.Context, root, run string) (*os.File, bool, error) {
	dir, err := os.Open(filepath.Join(root, run))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		wrapped := fmt.Errorf("open drill run directory %s: %w", run, err)
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.lock_failed",
			slog.String("err", wrapped.Error()),
		)

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
		slog.ErrorContext(
			ctx,
			"backup.restore_drill.lock_failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, false, wrapped
	}

	return dir, false, nil
}

// releaseDrillRun closes the run's lock after teardown removes its scratch
// directory.
func releaseDrillRun(r *restoreDrillCtx) {
	if r.runLock != nil {
		_ = r.runLock.Close()
		r.runLock = nil
	}
}
