// The orphan sweep removes resources from restore drills that stopped before
// cleanup, including drills that exceeded their memory limit. Such runs can
// leave scratch engines, scratch data, and staged artifacts. The sweep reads
// run IDs from resource names and removes resources when another process does
// not own the run's lock. A running drill's lock prevents that removal.

package ops

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// drillRunIDPattern recognizes the run IDs that RunBackupRestoreDrill creates.
// The sweep accepts a discovered run path only when its ID matches.
var drillRunIDPattern = regexp.MustCompile(`^rt[0-9]{8}T[0-9]{6}Z-[0-9]+$`)

// drillContainerPrefixes start scratch-engine names. Each name ends with the
// run ID.
var drillContainerPrefixes = []string{"tack-rtfdb-", "tack-rtyb-"}

// drillStagePrefixes start staging-directory names directly under the backup
// root. Each name ends with the run ID.
var drillStagePrefixes = []string{"restore-drill-yb-", "restore-drill-chain-"}

// drillOrphan records an earlier run's scratch containers and an engine image
// that can remove its scratch directory.
type drillOrphan struct {
	containers []string
	image      string
}

// sweepDrillOrphans attempts to remove earlier runs' scratch engines, scratch
// directories, and staging directories after checking each run's lock. It
// logs resources selected for removal and live runs it skips. Scratch-removal
// errors and staging-directory RemoveAll errors produce logs. ContainerRemove
// errors and staging-path Stat errors do not. Cleanup failures do not stop the
// current drill.
func sweepDrillOrphans(ctx context.Context, r *restoreDrillCtx) {
	orphans := findDrillRuns(ctx, r)
	delete(orphans, r.RunID)
	var removed, skipped []string
	for _, run := range slices.Sorted(maps.Keys(orphans)) {
		lock, live, err := tryLockDrillRun(ctx, drillScratchRoot(r), run)
		if err != nil {
			continue
		}
		if live {
			skipped = append(skipped, run)
			continue
		}
		removed = append(removed, removeDrillOrphan(ctx, r, run, orphans[run])...)
		if lock != nil {
			_ = lock.Close()
		}
	}
	if len(removed) > 0 || len(skipped) > 0 {
		telemetry.L(ctx).InfoContext(ctx, "backup.restore_drill.orphans_swept",
			slog.String("removed", strings.Join(removed, " ")),
			slog.String("live_runs_skipped", strings.Join(skipped, " ")))
	}
}

// findDrillRuns finds scratch engines, scratch directories, and staging
// directories associated with this backup root. It groups their run IDs and
// container names by run.
func findDrillRuns(ctx context.Context, r *restoreDrillCtx) map[string]*drillOrphan {
	logger := telemetry.L(ctx)
	runs := map[string]*drillOrphan{}
	add := func(run string) *drillOrphan {
		if runs[run] == nil {
			runs[run] = &drillOrphan{containers: nil, image: ""}
		}
		return runs[run]
	}
	listed, err := r.Cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("name", "tack-rt"),
	})
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.orphan_list_failed", slog.String("err", err.Error()))
	}
	for _, item := range listed.Items {
		for _, name := range item.Names {
			name = strings.TrimPrefix(name, "/")
			run, ok := drillRunFromName(name, drillContainerPrefixes)
			if !ok || !drillEngineUnderBackupRoot(r, item.Mounts, run) {
				continue
			}
			orphan := add(run)
			orphan.containers = append(orphan.containers, name)
			orphan.image = item.Image
		}
	}
	for _, entry := range readDirEntries(ctx, drillScratchRoot(r)) {
		if entry.IsDir() && drillRunIDPattern.MatchString(entry.Name()) {
			add(entry.Name())
		}
	}
	for _, entry := range readDirEntries(ctx, r.Cfg.BackupRoot) {
		if run, ok := drillRunFromName(entry.Name(), drillStagePrefixes); ok && entry.IsDir() {
			add(run)
		}
	}
	return runs
}

// drillEngineUnderBackupRoot checks whether an engine belongs to this backup
// root. A bind-mount source under the root or an existing run scratch path
// establishes the association. The sweep excludes engines that lack either
// association, as required by TACK-563.
func drillEngineUnderBackupRoot(r *restoreDrillCtx, mounts []container.MountPoint, run string) bool {
	backupRoot := filepath.Clean(r.Cfg.BackupRoot) + string(filepath.Separator)
	for _, point := range mounts {
		if strings.HasPrefix(filepath.Clean(point.Source)+string(filepath.Separator), backupRoot) {
			return true
		}
	}
	_, err := os.Stat(filepath.Join(drillScratchRoot(r), run))
	return err == nil
}

// drillRunFromName returns a valid run ID after one of the supplied prefixes
// in name.
func drillRunFromName(name string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		run, found := strings.CutPrefix(name, prefix)
		if found && drillRunIDPattern.MatchString(run) {
			return run, true
		}
	}
	return "", false
}

// readDirEntries returns the entries in dir. A missing directory returns an
// empty result without an error log. Other listing errors produce a log entry.
func readDirEntries(ctx context.Context, dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.orphan_list_failed",
			slog.String("dir", dir), slog.String("err", err.Error()))
	}
	return entries
}

// removeDrillOrphan attempts engine removal before deleting scratch and
// staging directories to stop engine writes before directory cleanup. It
// returns container names selected for removal and staging paths it removed.
// Scratch-directory removal logs its own result.
func removeDrillOrphan(ctx context.Context, r *restoreDrillCtx, run string, orphan *drillOrphan) []string {
	removed := make([]string, 0, len(orphan.containers)+len(drillStagePrefixes))
	for _, name := range orphan.containers {
		removeContainerForce(ctx, r.Cli, name)
		removed = append(removed, name)
	}
	if _, err := os.Stat(filepath.Join(drillScratchRoot(r), run)); err == nil {
		image := orphan.image
		if image == "" {
			image = r.Cfg.BackupYBImage
		}
		removeDrillScratchRun(ctx, r, image, run)
	}
	for _, prefix := range drillStagePrefixes {
		dir := filepath.Join(r.Cfg.BackupRoot, prefix+run)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.scratch_leaked",
				slog.String("dir", dir), slog.String("err", err.Error()))
			continue
		}
		removed = append(removed, dir)
	}
	return removed
}
