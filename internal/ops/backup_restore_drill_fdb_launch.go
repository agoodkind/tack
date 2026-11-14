// FoundationDB restore launch runs in the background and writes output and
// exit status to separate files. Monitoring can poll without imposing an
// overall restore deadline.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// fdbRestoreFiles specifies the background restore's files in the scratch
// container. Tests use a temporary directory to verify launch behavior.
type fdbRestoreFiles struct {
	// Log records restore output.
	Log string
	// Exit records the completed restore's exit status.
	Exit string
	// Partial stores the status before an atomic rename to Exit.
	Partial string
}

// drillFDBRestoreFiles returns the scratch container's restore file paths.
func drillFDBRestoreFiles() fdbRestoreFiles {
	return fdbRestoreFiles{
		Log:     fdbRestoreLogPath,
		Exit:    fdbRestoreExitPath,
		Partial: fdbRestoreExitTemp,
	}
}

// fdbRestoreLaunchScript starts the restore from shell positional parameters.
// Expansion with "$@" preserves every argument without parsing untrusted
// destination URLs as shell code. File paths use quoted internal constants.
//
// Cleanup failure must stop launch; otherwise monitoring could read a prior
// run's status or output. The shell cannot use set -e because it must record a
// failed restore's exit status. Output redirection and background execution
// release exec streams while fdbrestore --waitfordone continues. A temporary
// status file and rename prevent readers from observing an incomplete status.
func fdbRestoreLaunchScript(files fdbRestoreFiles) string {
	log, exit, partial := shellQuote(files.Log), shellQuote(files.Exit), shellQuote(files.Partial)

	return "rm -f " + log + " " + exit + " " + partial + " || exit 1; " +
		`{ "$@"; echo $? >` + partial + "; " +
		"mv " + partial + " " + exit + "; } >" + log + " 2>&1 &"
}

// fdbRestoreLaunchArgv builds the shell program, its $0 label, and the restore
// arguments that the program expands with "$@".
func fdbRestoreLaunchArgv(files fdbRestoreFiles, restore []string) []string {
	argv := []string{"sh", "-c", fdbRestoreLaunchScript(files), "fdbrestore-launch"}
	return append(argv, restore...)
}

// launchFDBRestore starts the restore in the background and returns after
// launch. Output redirection releases exec streams. Monitoring detects stalls
// without limiting total work. Positional arguments prevent shell parsing of
// the destination URL.
func launchFDBRestore(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	restore []string,
) error {
	logger := telemetry.L(ctx)
	argv := fdbRestoreLaunchArgv(drillFDBRestoreFiles(), restore)
	res, err := containerExec(ctx, r.Cli, container, argv)
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf(
			"launch fdbrestore: exit %d: %s: %w",
			res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stderr)),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(ctx, "backup.restore_drill.fdb.restore_started")

	return nil
}
