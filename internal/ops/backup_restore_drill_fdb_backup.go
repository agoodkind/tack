// backup_restore_drill_fdb_backup.go picks which FoundationDB backup session
// the drill restores from. Every `fdb-continuous-init` that starts a session
// registers a new backup name in the store, and each session's restorable
// window begins when it started, so a moment retained only by an older
// session is one the newest session cannot reach. The drill therefore reads
// the window of each session, newest first, and restores from the first one
// that covers the target; without a target it restores the newest, as before.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// fdbBackupMarkerPrefix is the folder the FoundationDB blobstore registers
// each backup under as a marker object, backups/<name>.
const fdbBackupMarkerPrefix = "backups/"

// selectFDBBackup returns the name of the backup the drill restores. With no
// target it is the newest marker, read without describing anything, so the
// default drill neither reads a window nor touches the source cluster. With a
// target it is the newest backup whose restorable window covers the target,
// checked before any restore starts, so a moment no backup can reach stops the
// drill instead of quietly restoring some other moment. markers are the
// backups/ marker keys sorted ascending.
func selectFDBBackup(ctx context.Context, r *restoreDrillCtx, containerName string, markers []string) (string, error) {
	names := fdbBackupNames(markers)
	if r.FDBTargetTime == nil {
		return names[len(names)-1], nil
	}
	logger := telemetry.L(ctx)
	target := *r.FDBTargetTime
	name, window, err := chooseFDBBackupForTarget(target, names,
		func(backupName string) (fdbRestorableWindow, error) {
			return describeFDBBackupWindow(ctx, r, containerName, backupName)
		},
		func(backupName, reason string) {
			logger.InfoContext(ctx, "backup.restore_drill.fdb.backup_skipped",
				slog.String("name", backupName), slog.String("reason", reason))
		})
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return "", err
	}
	logger.InfoContext(ctx, "backup.restore_drill.fdb.target_in_window",
		slog.String("name", name),
		slog.String("target", formatFDBTime(target)),
		slog.String("window_min", formatFDBTime(window.Min)),
		slog.String("window_max", formatFDBTime(window.Max)),
	)
	return name, nil
}

// fdbBackupNames turns the backups/ marker keys, in the order the store listed
// them, into the bare names fdbrestore addresses, keeping that order.
func fdbBackupNames(markers []string) []string {
	names := make([]string, 0, len(markers))
	for _, marker := range markers {
		names = append(names, strings.TrimPrefix(marker, fdbBackupMarkerPrefix))
	}
	return names
}

// chooseFDBBackupForTarget walks the backup names newest first and returns
// the first whose window covers the target, with that window. The search stops
// there: when more than one backup covers the target, the newest is used and
// the older ones are never described, because any covering backup restores
// the same moment and the newest is the one the drill restores without a
// target. A backup whose window cannot be read is passed over with its reason
// handed to skip rather than ending the search, since an older backup may
// still cover the target. When none does, the error names every backup with
// its window or the reason it could not be read, so the operator can pick a
// moment that works.
func chooseFDBBackupForTarget(
	target time.Time,
	names []string,
	describe func(backupName string) (fdbRestorableWindow, error),
	skip func(backupName, reason string),
) (string, fdbRestorableWindow, error) {
	reasons := make([]string, 0, len(names))
	for _, name := range slices.Backward(names) {
		window, err := describe(name)
		if err != nil {
			skip(name, err.Error())
			reasons = append(reasons, name+": "+err.Error())
			continue
		}
		if err := assertTargetWithinWindow(target, window); err != nil {
			skip(name, err.Error())
			reasons = append(reasons, fmt.Sprintf("%s: restorable window %s .. %s",
				name, formatFDBTime(window.Min), formatFDBTime(window.Max)))
			continue
		}
		return name, window, nil
	}
	return "", fdbRestorableWindow{}, fmt.Errorf(
		"restore target %s is outside the restorable window of every FoundationDB backup in the store (%s)",
		formatFDBTime(target), strings.Join(reasons, "; "))
}
