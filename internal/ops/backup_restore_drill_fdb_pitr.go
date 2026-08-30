// backup_restore_drill_fdb_pitr.go restores FoundationDB to an operator-chosen
// moment instead of the latest restorable point. It assembles the two engine
// command lines the drill runs, and refuses a target the backup cannot reach
// before any restore starts.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// fdbScratchClusterFile is the throwaway cluster's own cluster file inside
	// the drill container, written by the fdb overlay at boot. Every restore
	// the drill runs writes here and nowhere else.
	fdbScratchClusterFile = "/var/fdb/fdb.cluster"

	// fdbOrigClusterFilePath is where the live cluster's file is readable
	// inside the throwaway container. fdbrestore converts a wall-clock target
	// into a database version using the source cluster's version metadata, so
	// a point-in-time restore needs the source's cluster file even though it
	// writes only to the destination.
	//
	// The path is deliberately not /etc/foundationdb/fdb.cluster, which is
	// FoundationDB's last-resort default when no cluster file is named. The
	// image ships no such directory, so today a command inside the throwaway
	// that forgot its cluster-file flag fails loudly. Putting the live file on
	// the default path would turn that same mistake into a write against
	// production.
	fdbOrigClusterFilePath = "/tack-orig-fdb/fdb.cluster"

	// fdbOrigClusterMount binds the live cluster's client cluster file into
	// the throwaway container read-only. The host side is the file the fdb
	// service writes and the continuous backup already reads.
	fdbOrigClusterMount = "/etc/foundationdb/fdb.cluster:" + fdbOrigClusterFilePath + ":ro"
)

// fdbRestoreShellCommand builds the shell command the drill runs inside the
// throwaway container. With the zero target time it restores the latest
// restorable point, which is all the drill could do before point-in-time
// restore existed. With a target time it adds --timestamp and
// --orig-cluster-file, the pair fdbrestore needs to turn a wall-clock moment
// into a version. The destination stays the throwaway's own cluster file
// either way.
//
// Flag names and the timestamp layout are foundationdb 7.4.6's own, from
// `fdbrestore --help` in the pinned image.
func fdbRestoreShellCommand(destURL string, targetTime time.Time, timeoutSeconds int) string {
	command := fmt.Sprintf(
		"timeout %d fdbrestore start --dest-cluster-file %s -r '%s' --waitfordone",
		timeoutSeconds, fdbScratchClusterFile, destURL)
	if targetTime.IsZero() {
		return command
	}
	return command + fmt.Sprintf(" --timestamp '%s' --orig-cluster-file %s",
		formatFDBTime(targetTime), fdbOrigClusterFilePath)
}

// fdbDescribeShellCommand builds the `fdbbackup describe` call that reads the
// backup's restorable window. --version-timestamps turns the reported versions
// into wall-clock times and needs a cluster file to do it; that cluster file is
// the source cluster, because the versions in the backup are the source's.
func fdbDescribeShellCommand(destURL string, timeoutSeconds int) string {
	return fmt.Sprintf(
		"timeout %d fdbbackup describe -d '%s' -C %s --version-timestamps",
		timeoutSeconds, destURL, fdbOrigClusterFilePath)
}

// assertFDBTargetRestorable refuses a target time the backup cannot reach,
// before the restore runs. A drill with no target time is a no-op here, so the
// default path neither reads the window nor touches the source cluster.
//
// The describe output embeds the blobstore credentials in the destination URL,
// so it is redacted on the error path and never logged whole.
func assertFDBTargetRestorable(ctx context.Context, r *restoreDrillCtx, containerName, destURL string) error {
	if r.FDBTargetTime.IsZero() {
		return nil
	}
	logger := telemetry.L(ctx)
	fail := func(err error) error {
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return err
	}
	res, err := containerExec(ctx, r.Cli, containerName, []string{
		"sh", "-c", fdbDescribeShellCommand(destURL, r.Cfg.BackupFDBTimeoutSeconds),
	})
	if err != nil {
		return fail(fmt.Errorf("fdbbackup describe exec: %w", err))
	}
	if res.ExitCode != 0 {
		return fail(fmt.Errorf("fdbbackup describe exited %d: %s", res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stdout+" "+res.Stderr))))
	}
	window, err := fdbRestorableWindowFromDescribe(res.Stdout)
	if err != nil {
		return fail(err)
	}
	if err := assertTargetWithinWindow(r.FDBTargetTime, window); err != nil {
		return fail(err)
	}
	logger.InfoContext(ctx, "backup.restore_drill.fdb.target_in_window",
		slog.String("target", formatFDBTime(r.FDBTargetTime)),
		slog.String("window_min", formatFDBTime(window.Min)),
		slog.String("window_max", formatFDBTime(window.Max)),
	)
	return nil
}

// assertTargetWithinWindow refuses a target the backup cannot reach, naming the
// window so the operator can pick a moment that works. Handing an out-of-window
// time to fdbrestore is exactly what this guard exists for: the restore would
// otherwise produce data from some other moment and report success.
func assertTargetWithinWindow(target time.Time, window fdbRestorableWindow) error {
	if target.Before(window.Min) || target.After(window.Max) {
		return fmt.Errorf(
			"restore target %s is outside the backup's restorable window %s .. %s",
			formatFDBTime(target), formatFDBTime(window.Min), formatFDBTime(window.Max))
	}
	return nil
}

// parseFDBTargetTime reads the operator's --fdb-target-time. Both accepted
// forms carry an explicit UTC offset, so a target time can never mean two
// moments depending on where it was typed. RFC 3339 is the form an operator
// writes; the FoundationDB form is the one they can copy straight out of
// `fdbbackup describe` or `fdbbackup status` output.
func parseFDBTargetTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, fdbStatusTimestampLayout} {
		at, err := time.Parse(layout, value)
		if err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("restore target time %q is not a time:"+
		" write it as RFC 3339 (2026-08-30T01:07:23Z)"+
		" or in FoundationDB's own form (2026/08/30.01:07:23+0000)", value)
}

// formatFDBTime renders a moment the way FoundationDB's own tools print and
// accept it, in UTC so a drill's logs and errors read the same everywhere.
func formatFDBTime(at time.Time) string {
	return at.UTC().Format(fdbStatusTimestampLayout)
}
