// Backup selection checks sessions from newest to oldest for a window that
// includes the requested FoundationDB restore moment. Each session started
// by fdb-continuous-init registers a new backup name. Its window starts with
// the session; an older session may retain a moment that a newer session
// does not cover. The drill restores the first covering session or the newest
// session when the operator omits a target.

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

// fdbBackupMarkerPrefix identifies the backups/<name> marker objects that
// FoundationDB registers for each backup.
const fdbBackupMarkerPrefix = "backups/"

// selectFDBBackup returns the selected backup name. markers contains
// backups/ marker keys in ascending order. Without a target, the function
// selects the newest marker without inspecting windows or querying the
// source cluster.
//
// With a target, selection checks windows before starting a restore and uses
// the newest covering backup. If every backup excludes the target, selection
// fails rather than restoring another moment.
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

// fdbBackupNames removes the backups/ prefix from marker keys for fdbrestore
// and preserves the store's listing order.
func fdbBackupNames(markers []string) []string {
	names := make([]string, 0, len(markers))
	for _, marker := range markers {
		names = append(names, strings.TrimPrefix(marker, fdbBackupMarkerPrefix))
	}
	return names
}

// chooseFDBBackupForTarget checks backup names newest first and returns the
// first covering backup and its window. It does not inspect older backups
// after a match. Every covering backup can restore the target moment, and the
// newest backup also serves the drill's default target.
//
// If a window read fails, skip receives the reason, and the search continues
// because an older backup may cover the target. If every backup fails, the
// error lists each backup with its window or read failure. The report helps
// the operator select a restorable moment.
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
