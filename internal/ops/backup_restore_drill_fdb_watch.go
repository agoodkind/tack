// backup_restore_drill_fdb_watch.go runs the FoundationDB restore in the
// scratch container and reads what it is doing. The restore runs in the
// background with its own output and exit status captured to files, so the
// drill can watch it work instead of blocking on one exec under a fixed time
// budget it would eventually outgrow.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// fdbRestoreLogPath captures the restore's own output, including the
	// progress updates --waitfordone prints.
	fdbRestoreLogPath = "/var/fdb/restore.log"
	// fdbRestoreExitPath holds the restore's exit status once it has one. It
	// is moved into place from the partial name below, so a reader sees either
	// nothing or the whole status, never a half-written one.
	fdbRestoreExitPath = "/var/fdb/restore.rc"
	fdbRestoreExitTemp = "/var/fdb/restore.rc.partial"
	// fdbRestoreLogTailBytes bounds how much of the restore's output an error
	// message carries.
	fdbRestoreLogTailBytes = 4096
)

// launchFDBRestore starts the restore vector it is handed in the background and
// returns as soon as it is running. The command group sends its own output to
// the log file, which releases the exec's streams and lets this call return
// while the restore works on. Nothing here bounds how long that takes; the
// watch decides when it has stopped.
//
// The vector is built elsewhere and arrives as the shell's positional
// parameters, so the destination URL inside it is never parsed as shell program
// text.
func launchFDBRestore(ctx context.Context, r *restoreDrillCtx, container string, restore []string) error {
	logger := telemetry.L(ctx)
	script := "rm -f " + fdbRestoreLogPath + " " + fdbRestoreExitPath + " " + fdbRestoreExitTemp + "; " +
		`{ "$@"; echo $? >` + fdbRestoreExitTemp + "; " +
		"mv " + fdbRestoreExitTemp + " " + fdbRestoreExitPath + "; } >" + fdbRestoreLogPath + " 2>&1 &"
	argv := append([]string{"sh", "-c", script, "fdbrestore-launch"}, restore...)
	res, err := containerExec(ctx, r.Cli, container, argv)
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf("launch fdbrestore: exit %d: %s: %w", res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stderr)), err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.fdb.restore_started")
	return nil
}

// fdbRestoreExitCode reports the restore's exit status once it has one.
func fdbRestoreExitCode(ctx context.Context, r *restoreDrillCtx, container string) (int, bool, error) {
	probe := "if [ -s " + fdbRestoreExitPath + " ]; then printf 'exited '; cat " + fdbRestoreExitPath +
		"; else printf 'running\\n'; fi"
	res, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", probe})
	if err != nil {
		wrapped := fmt.Errorf("read the fdbrestore exit status: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()))
		return 0, false, wrapped
	}
	if res.ExitCode != 0 {
		return 0, false, fmt.Errorf("read the fdbrestore exit status: exit %d: %s",
			res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return parseFDBRestoreExit(res.Stdout)
}

// parseFDBRestoreExit reads the exit-status probe's answer: either the restore
// is still running, or it exited with the status the probe printed. Output
// that says neither is an error, because this probe is what tells the wait a
// restore finished and an answer nobody could read must never pass for one
// that is still running.
func parseFDBRestoreExit(out string) (int, bool, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "running" {
		return 0, false, nil
	}
	status, found := strings.CutPrefix(trimmed, "exited ")
	if !found {
		return 0, false, fmt.Errorf("read %q as the fdbrestore exit status", trimmed)
	}
	code, err := strconv.Atoi(strings.TrimSpace(status))
	if err != nil {
		return 0, false, fmt.Errorf("read %q as the fdbrestore exit status: it names no exit code", trimmed)
	}
	return code, true, nil
}

// fdbRestoreStatusText returns one `fdbrestore status` reading from the
// scratch cluster. The reading can name the blobstore URL, so it is redacted
// before it reaches an error.
func fdbRestoreStatusText(ctx context.Context, r *restoreDrillCtx, container string) (string, error) {
	res, err := containerExec(ctx, r.Cli, container,
		[]string{"fdbrestore", "status", "--dest-cluster-file", fdbScratchClusterFile})
	if err != nil {
		wrapped := fmt.Errorf("read the fdbrestore status: %w", err)
		// A status read that fails is tolerated for the stall window rather
		// than failing the drill outright, so it is warned rather than
		// errored; the stall report carries the last one.
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.fdb.status_unreadable",
			slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("fdbrestore status exited %d: %s", res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stderr)))
	}
	return res.Stdout, nil
}

// fdbRestoreLogTail returns the end of the restore's own output, redacted, for
// an error message. A tail it could not read says so rather than reading as an
// empty restore log.
func fdbRestoreLogTail(ctx context.Context, r *restoreDrillCtx, container string) string {
	res, err := containerExec(ctx, r.Cli, container,
		[]string{"tail", "-c", strconv.Itoa(fdbRestoreLogTailBytes), fdbRestoreLogPath})
	if err != nil || res.ExitCode != 0 {
		return "the restore log could not be read"
	}
	tail := strings.TrimSpace(res.Stdout)
	if tail == "" {
		return "the restore log is empty"
	}
	return redactSecret(r.Cfg, tail)
}
