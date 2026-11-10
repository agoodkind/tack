// The restore drill stores temporary engine data in a per-run directory under
// the backup root and removes it during cleanup. It uses a bind mount instead
// of a Docker volume or container layer. The original deployment used the
// guest's slow storage tier for this root, separate from the live ledger's fast
// pool. The configured backup root controls that storage placement.

package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// drillScratchDirName identifies the directory under the backup root with one
	// subdirectory per drill run.
	drillScratchDirName = "drill-scratch"
	// drillScratchMount selects the removal container's mount path for the scratch
	// root.
	drillScratchMount = "/scratch"
	// drillScratchRemoveTimeout limits removal time for one run's scratch tree.
	// The tree can contain a complete restored dataset on slow disks.
	drillScratchRemoveTimeout = 5 * time.Minute
)

// drillScratchRunDir returns the run's directory under the backup scratch
// root. tack-ops mounts the backup root at the guest's path. The result works
// both inside tack-ops and as a daemon bind-mount source.
//
// The path has this layout:
//
//	<BackupRoot>/drill-scratch/<RunID>
func drillScratchRunDir(r *restoreDrillCtx) string {
	return filepath.Join(drillScratchRoot(r), r.RunID)
}

// makeDrillScratchDir creates the requested subdirectory under the run and
// returns its path. image identifies the engine that will write there. The
// first image becomes the cleanup image, avoiding a separate image pull.
//
// The drill user creates and owns the directory with mode 0755 before umask
// adjustments. The scratch engine images use root rather than another USER.
// The deployment relies on container root matching guest root, as its backup
// staging directories also do.
func makeDrillScratchDir(ctx context.Context, r *restoreDrillCtx, image string, parts ...string) (string, error) {
	dir := filepath.Join(append([]string{drillScratchRunDir(r)}, parts...)...)
	if r.scratchImage == "" {
		r.scratchImage = image
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		wrapped := fmt.Errorf("mkdir drill scratch %s: %w", dir, err)
		slog.ErrorContext(ctx, "backup.restore_drill.scratch_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return dir, nil
}

// removeDrillScratch attempts to delete the run's scratch directory. Scratch
// engines write root-owned files, so cleanup uses root in a temporary container
// when an engine image exists. It uses the engines' network, which the daemon
// already accepted. A failed removal logs the directory for manual cleanup.
//
// Without a recorded image, the drill tries to remove the empty directory from
// claimDrillRun itself.
func removeDrillScratch(ctx context.Context, r *restoreDrillCtx) {
	if r.scratchImage != "" {
		removeDrillScratchRun(ctx, r, r.scratchImage, r.RunID)
		return
	}
	runDir := drillScratchRunDir(r)
	if err := os.Remove(runDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.scratch_leaked",
			slog.String("dir", runDir), slog.String("err", err.Error()))
	}
}

// removeDrillScratchRun deletes the specified run's scratch directory with a
// temporary container using image. The target can be the current run or a
// stopped run that left resources behind.
func removeDrillScratchRun(ctx context.Context, r *restoreDrillCtx, image, run string) {
	logger := telemetry.L(ctx)
	removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drillScratchRemoveTimeout)
	defer cancel()
	runDir := filepath.Join(drillScratchRoot(r), run)
	res, err := runOneShot(removeCtx, r.Cli, logger, runOneShotOptions{
		Image:      image,
		Network:    r.Cfg.BackupFDBNetwork,
		Entrypoint: []string{"rm"},
		Cmd:        []string{"-rf", "--", path.Join(drillScratchMount, run)},
		Env:        nil,
		Binds:      []string{drillScratchRoot(r) + ":" + drillScratchMount},
		ExtraHosts: nil,
		Name:       "",
	})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("rm exited %d: %s", res.ExitCode, res.Stderr)
	}
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.scratch_leaked",
			slog.String("dir", runDir), slog.String("err", err.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.restore_drill.scratch_removed", slog.String("dir", runDir))
}
