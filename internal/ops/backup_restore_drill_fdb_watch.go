// FoundationDB restore monitoring reads exit status, progress, and bounded
// diagnostic output from the background process's files.

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
	// fdbRestoreLogPath records restore output and --waitfordone updates.
	fdbRestoreLogPath = "/var/fdb/restore.log"
	// fdbRestoreExitPath records the complete status after an atomic rename
	// from the partial file, preventing incomplete reads.
	fdbRestoreExitPath = "/var/fdb/restore.rc"
	fdbRestoreExitTemp = "/var/fdb/restore.rc.partial"
	// fdbRestoreLogTailBytes limits restore output included in errors.
	fdbRestoreLogTailBytes = 4096
)

// fdbRestoreExitCode reports the completed restore's exit status.
func fdbRestoreExitCode(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
) (int, bool, error) {
	probe := "if [ -s " + fdbRestoreExitPath + " ]; then printf 'exited '; cat " + fdbRestoreExitPath +
		"; else printf 'running\\n'; fi"
	res, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", probe})
	if err != nil {
		wrapped := fmt.Errorf("read the fdbrestore exit status: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, false, wrapped
	}

	if res.ExitCode != 0 {
		return 0, false, fmt.Errorf(
			"read the fdbrestore exit status: exit %d: %s",
			res.ExitCode,
			strings.TrimSpace(res.Stderr),
		)
	}

	return parseFDBRestoreExit(res.Stdout)
}

// parseFDBRestoreExit accepts running or an exited integer status. Malformed
// output must fail instead of incorrectly indicating an active restore.
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
		return 0, false, fdbRestoreExitIntegerMissingError{Output: trimmed}
	}

	return code, true, nil
}

type fdbRestoreExitIntegerMissingError struct {
	Output string
}

func (failure fdbRestoreExitIntegerMissingError) Error() string {
	return fmt.Sprintf(
		"read %q as the fdbrestore exit status: it does not include an integer exit code",
		failure.Output,
	)
}

// fdbRestoreStatusText reads redacted scratch status. FoundationDB 7.4.6's
// RestoreConfig::getFullStatus_impl appends the original source URL, including
// credentials, to every tag line. Both success and failure paths require
// redaction before logging; progress counters remain available.
func fdbRestoreStatusText(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
) (string, error) {
	res, err := containerExec(
		ctx,
		r.Cli,
		container,
		[]string{"fdbrestore", "status", "--dest-cluster-file", fdbScratchClusterFile},
	)
	if err != nil {
		wrapped := fmt.Errorf("read the fdbrestore status: %w", err)
		// A failed read permits retries during the inactivity window.
		// The eventual stall error includes the final failed read.
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.fdb.status_unreadable",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	return fdbRestoreStatusReading(r.Cfg, res)
}

// fdbRestoreStatusReading redacts credentials from successful output and
// failed-command stderr before returning either result.
func fdbRestoreStatusReading(cfg *config.Config, res execResult) (string, error) {
	if res.ExitCode != 0 {
		return "", fmt.Errorf(
			"fdbrestore status exited %d: %s",
			res.ExitCode,
			redactSecret(cfg, strings.TrimSpace(res.Stderr)),
		)
	}

	return redactSecret(cfg, res.Stdout), nil
}

// fdbRestoreLogTail returns bounded, redacted output for errors and
// distinguishes failed reads from an empty log.
func fdbRestoreLogTail(ctx context.Context, r *restoreDrillCtx, container string) string {
	res, err := containerExec(
		ctx,
		r.Cli,
		container,
		[]string{"tail", "-c", strconv.Itoa(fdbRestoreLogTailBytes), fdbRestoreLogPath},
	)
	if err != nil || res.ExitCode != 0 {
		return "the drill could not read the restore log"
	}

	tail := strings.TrimSpace(res.Stdout)
	if tail == "" {
		return "the restore log is empty"
	}

	return redactSecret(r.Cfg, tail)
}
