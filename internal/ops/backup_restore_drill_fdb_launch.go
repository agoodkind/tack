// The FoundationDB restore runs in the background inside the scratch
// container. Files record its output and exit status for the drill to monitor.
// A fixed deadline for one blocking exec would eventually reject a restore
// as the dataset grows.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// fdbRestoreFiles specifies the three files that a background restore writes
// inside the scratch container. Tests supply a temporary directory and run the
// launch program to check its behavior instead of asserting its text.
type fdbRestoreFiles struct {
	// Log captures the restore's own output.
	Log string
	// Exit records the restore's exit status after the command finishes.
	Exit string
	// Partial records the exit status before the launch renames it to Exit.
	// Readers cannot observe an incomplete status in Exit.
	Partial string
}

// drillFDBRestoreFiles returns the file layout for the scratch container.
func drillFDBRestoreFiles() fdbRestoreFiles {
	return fdbRestoreFiles{
		Log:     fdbRestoreLogPath,
		Exit:    fdbRestoreExitPath,
		Partial: fdbRestoreExitTemp,
	}
}

// fdbRestoreLaunchScript starts the supplied argument vector in the
// background, redirects its output to the log, and publishes its exit status.
// The shell receives the vector as positional parameters and executes "$@".
// It does not parse external argument values as shell syntax. The three file
// names use the drill's constants, and each name also uses single-word quoting.
//
// The initial file removal uses || exit 1. A failed removal must stop the
// launch: otherwise the watch could read the previous run's exit status or
// report its log as the current restore's output. The program cannot use
// set -e because a failed restore must still execute echo $? to record its
// own status.
//
// The shell supplies background execution and file publication that the
// engine CLI does not offer. fdbrestore --waitfordone can run for as long as
// the dataset requires. Redirecting the group's streams to a file and using &
// lets the launching exec return while the watch polls. The group writes
// echo $? to a temporary file and renames that file to publish a complete
// status. Only the command's arguments include external values.
func fdbRestoreLaunchScript(files fdbRestoreFiles) string {
	log, exit, partial := shellQuote(files.Log), shellQuote(files.Exit), shellQuote(files.Partial)

	return "rm -f " + log + " " + exit + " " + partial + " || exit 1; " +
		`{ "$@"; echo $? >` + partial + "; " +
		"mv " + partial + " " + exit + "; } >" + log + " 2>&1 &"
}

// fdbRestoreLaunchArgv builds the exec arguments: the launch program, its
// $0 label, and the restore vector that the shell expands as "$@".
func fdbRestoreLaunchArgv(files fdbRestoreFiles, restore []string) []string {
	argv := []string{"sh", "-c", fdbRestoreLaunchScript(files), "fdbrestore-launch"}
	return append(argv, restore...)
}

// launchFDBRestore starts the supplied restore vector in the background.
// The command group redirects its output to the log and releases the exec's
// streams, allowing this call to return while the restore continues. The
// launch does not limit total restore time; the watch detects inactivity.
//
// The caller builds the vector. The shell receives its arguments as positional
// parameters and does not parse the destination URL as shell program text.
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
