// The watch reads the background FoundationDB restore's exit status, current
// status, and log tail for errors. The launch starts the restore separately
// and records its output and exit status in files.

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
	// fdbRestoreLogPath records the restore's output, including progress updates
	// from --waitfordone.
	fdbRestoreLogPath = "/var/fdb/restore.log"
	// fdbRestoreExitPath records the completed restore's exit status. The launch
	// renames the partial file to this path. Readers observe an absent file or
	// the complete status rather than an incomplete write.
	fdbRestoreExitPath = "/var/fdb/restore.rc"
	fdbRestoreExitTemp = "/var/fdb/restore.rc.partial"
	// fdbRestoreLogTailBytes limits restore output in an error message.
	fdbRestoreLogTailBytes = 4096
)

// fdbRestoreExitCode reports the restore's exit status after it exits.
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

// parseFDBRestoreExit reads a running result or the completed restore's
// printed exit status. It rejects any other output. An unreadable completion
// probe must not count as evidence that the restore still runs.
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

// fdbRestoreStatusText reads and redacts one scratch-cluster status. The
// original FoundationDB 7.4.6 observation identifies
// RestoreConfig::getFullStatus_impl in fdbclient/FileBackupAgent.actor.cpp:
// each tag's status appends "URL: <source container>". That source is the
// original blobstore URL, including its access key and secret.
//
// Both success and error readings require redaction before later logging or
// error reporting. The watch reads counters that redaction preserves.
func fdbRestoreStatusText(ctx context.Context, r *restoreDrillCtx, container string) (string, error) {
	res, err := containerExec(ctx, r.Cli, container,
		[]string{"fdbrestore", "status", "--dest-cluster-file", fdbScratchClusterFile})
	if err != nil {
		wrapped := fmt.Errorf("read the fdbrestore status: %w", err)
		// The wait tolerates a failed status read until the stall window expires.
		// This path logs a warning, and the stall report includes the last failure.
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.fdb.status_unreadable",
			slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return fdbRestoreStatusReading(r.Cfg, res)
}

// fdbRestoreStatusReading converts the status exec result to a watch reading
// and removes credentials from the output stream that supplies the result.
func fdbRestoreStatusReading(cfg *config.Config, res execResult) (string, error) {
	if res.ExitCode != 0 {
		return "", fmt.Errorf("fdbrestore status exited %d: %s", res.ExitCode,
			redactSecret(cfg, strings.TrimSpace(res.Stderr)))
	}
	return redactSecret(cfg, res.Stdout), nil
}

// fdbRestoreLogTail returns a redacted restore-log tail for error reporting.
// It reports an unreadable log explicitly instead of reporting an empty log.
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
