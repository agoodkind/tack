// backup_restore_drill_scratch.go places the restore drill's throwaway engine
// data in a per-run directory under the backup root instead of a Docker volume
// or a container's own layer, and removes that directory at teardown. The
// backup root is the guest's slow storage tier, so a drill's restored dataset
// never lands on the fast pool the live ledger uses.

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
	// drillScratchDirName is the directory under the backup root holding one
	// subdirectory per drill run.
	drillScratchDirName = "drill-scratch"
	// drillScratchMount is where the removal container sees
	// drillScratchDirName.
	drillScratchMount = "/scratch"
	// drillScratchRemoveTimeout bounds the removal of one run's scratch tree,
	// which can hold a whole restored dataset on slow disks.
	drillScratchRemoveTimeout = 5 * time.Minute
)

// drillScratchRunDir returns the run's scratch directory,
// <BackupRoot>/drill-scratch/<RunID>. The drill runs in tack-ops, which
// mounts the backup root at the same path the guest uses, so the path is valid
// both here and as a bind-mount source for the daemon.
func drillScratchRunDir(r *restoreDrillCtx) string {
	return filepath.Join(drillScratchRoot(r), r.RunID)
}

// makeDrillScratchDir creates the directory at parts under the run's scratch
// directory and returns its path. image is the engine image about to write
// there; the first one recorded is the image teardown removes the tree with,
// so the removal never pulls an image the run did not already need.
//
// The directory is left owned by the drill's own user with the default mode.
// Both scratch engine images run as root (neither sets a USER other than
// root), and root in a container is root on the guest, the same identity
// every backup stage directory under the backup root already relies on.
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

// removeDrillScratch deletes the run's scratch directory on best effort. The
// scratch engines ran as root, so the files they wrote belong to root; the
// removal runs as root in a one-shot container, so teardown works whatever
// user the drill itself runs as. It joins the scratch engines' network, the
// one network the drill already knows the daemon can attach to. A failed
// removal is logged with the directory to delete by hand. A run whose engines
// never started holds only the empty directory claimDrillRun made, which the
// drill removes itself.
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

// removeDrillScratchRun deletes run's scratch directory with a one-shot
// container of image, for this drill's own run or for one a killed drill left.
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
