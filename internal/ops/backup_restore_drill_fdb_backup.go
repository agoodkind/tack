// FoundationDB backup sessions retain different time windows. Targeted
// restores inspect sessions newest first and select the first window that
// includes the target. Untargeted restores use the newest session.

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

// fdbBackupMarkerPrefix specifies the marker object folder, backups/<name>.
const fdbBackupMarkerPrefix = "backups/"

// selectFDBBackup selects a backup from ascending marker keys. Without a
// target it uses the newest marker without reading windows or the source
// cluster. With a target it verifies a covering window before restoring and
// refuses the target if every backup excludes it.
func selectFDBBackup(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	markers []string,
) (string, error) {
	names := fdbBackupNames(markers)
	if r.FDBTargetTime == nil {
		return names[len(names)-1], nil
	}

	logger := telemetry.L(ctx)
	target := *r.FDBTargetTime
	name, window, err := chooseFDBBackupForTarget(
		target,
		names,
		func(backupName string) (fdbRestorableWindow, error) {
			return describeFDBBackupWindow(ctx, r, containerName, backupName)
		},
		func(backupName, reason string) {
			logger.InfoContext(
				ctx,
				"backup.restore_drill.fdb.backup_skipped",
				slog.String("name", backupName),
				slog.String("reason", reason),
			)
		},
	)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return "", err
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.fdb.target_in_window",
		slog.String("name", name),
		slog.String("target", formatFDBTime(target)),
		slog.String("window_min", formatFDBTime(window.Min)),
		slog.String("window_max", formatFDBTime(window.Max)),
	)

	return name, nil
}

// fdbBackupNames removes marker prefixes without changing listing order.
func fdbBackupNames(markers []string) []string {
	names := make([]string, 0, len(markers))
	for _, marker := range markers {
		names = append(names, strings.TrimPrefix(marker, fdbBackupMarkerPrefix))
	}

	return names
}

// chooseFDBBackupForTarget selects the newest covering window and stops
// inspecting older backups after success. Unreadable windows invoke skip and
// permit checking older sessions. Failure reports every session's window or
// read error to support selecting another target.
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
			reasons = append(
				reasons,
				fmt.Sprintf(
					"%s: restorable window %s .. %s",
					name,
					formatFDBTime(window.Min),
					formatFDBTime(window.Max),
				),
			)
			continue
		}

		return name, window, nil
	}

	return "", fdbRestorableWindow{}, fmt.Errorf(
		"restore target %s is outside the restorable window of every FoundationDB backup in the store (%s)",
		formatFDBTime(target),
		strings.Join(reasons, "; "),
	)
}
