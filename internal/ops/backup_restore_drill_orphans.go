// backup_restore_drill_orphans.go removes what a killed restore drill left
// behind. A drill that dies before its teardown, such as one the kernel kills
// at its memory limit, leaves its scratch engines running, its scratch
// directory full, and its staged artifacts on disk. The next drill finds them
// by the run id in their names and removes every run whose lock no live drill
// holds.

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

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// drillRunIDPattern matches the run ids RunBackupRestoreDrill makes, so a
// name the sweep reads becomes a path only when it is one.
var drillRunIDPattern = regexp.MustCompile(`^rt[0-9]{8}T[0-9]{6}Z-[0-9]+$`)

// drillContainerPrefixes open the names of a run's scratch engines, which end
// in the run id.
var drillContainerPrefixes = []string{"tack-rtfdb-", "tack-rtyb-"}

// drillStagePrefixes open the names of the directories a run stages files in
// directly under the backup root, which end in the run id.
var drillStagePrefixes = []string{"restore-drill-yb-", "restore-drill-chain-"}

// drillOrphan is one earlier run's leftovers: its scratch engines, and the
// image one of them ran, which removes the run's scratch directory.
type drillOrphan struct {
	containers []string
	image      string
}

// sweepDrillOrphans removes the scratch engines, the scratch directory, and
// the staging directories of every earlier run whose lock is free, and logs
// one line naming every container and directory it removed and every run it
// left to a live drill. It is best effort: a failure is logged and the drill
// goes on.
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

// findDrillRuns returns every run id named by a scratch engine, a scratch
// directory, or a staging directory, with that run's engines.
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
			if run, ok := drillRunFromName(name, drillContainerPrefixes); ok {
				orphan := add(run)
				orphan.containers = append(orphan.containers, name)
				orphan.image = item.Image
			}
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

// drillRunFromName returns the run id that follows one of prefixes in name.
func drillRunFromName(name string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		run, found := strings.CutPrefix(name, prefix)
		if found && drillRunIDPattern.MatchString(run) {
			return run, true
		}
	}
	return "", false
}

// readDirEntries lists dir, logging and returning nothing when it cannot.
func readDirEntries(ctx context.Context, dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.orphan_list_failed",
			slog.String("dir", dir), slog.String("err", err.Error()))
	}
	return entries
}

// removeDrillOrphan removes one dead run's engines first, so nothing still
// writes into its directories, then its scratch and staging directories, and
// returns the names of the containers and the paths of the staging
// directories it removed. The scratch directory's removal logs itself.
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
