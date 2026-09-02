// backup_restore_drill_fdb_watch.go reads what the backgrounded FoundationDB
// restore is doing: whether it has exited and with what, one status reading,
// and the tail of its log for an error. The launch that puts the restore in
// the background with its output and exit status captured to files lives
// beside it.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
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
// scratch cluster, redacted. Every reading names the blobstore URL, access key
// and secret included, because foundationdb 7.4.6's restore status appends
// "URL: <source container>" to each tag's line (RestoreConfig::getFullStatus_impl
// in fdbclient/FileBackupAgent.actor.cpp) and the source container is the URL
// the restore was started with. Redacting here, on the success path as well as
// the error path, is what keeps the reading safe to carry into any log or
// error downstream; the watch reads only the counters out of it, and they
// survive the redaction untouched.
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
	return fdbRestoreStatusReading(r.Cfg, res)
}

// fdbRestoreStatusReading turns the status exec's result into the reading the
// watch gets, with the credentials removed from whichever stream carries them.
func fdbRestoreStatusReading(cfg *config.Config, res execResult) (string, error) {
	if res.ExitCode != 0 {
		return "", fmt.Errorf("fdbrestore status exited %d: %s", res.ExitCode,
			redactSecret(cfg, strings.TrimSpace(res.Stderr)))
	}
	return redactSecret(cfg, res.Stdout), nil
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
