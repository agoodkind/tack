// backup_restore_drill_fdb_launch.go runs the FoundationDB restore in the
// scratch container in the background, with its own output and exit status
// captured to files, so the drill can watch it work instead of blocking on one
// exec under a fixed time budget it would eventually outgrow.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// fdbRestoreFiles names the three files one backgrounded restore writes inside
// the scratch container. It travels as a value so the launch program can be
// built and run against a throwaway directory, which is how the launch's
// behavior is proven rather than its text asserted.
type fdbRestoreFiles struct {
	// Log captures the restore's own output.
	Log string
	// Exit holds the restore's exit status once it has one.
	Exit string
	// Partial is the name that status is written under before it is moved to
	// Exit, so a reader sees either nothing or the whole status.
	Partial string
}

// drillFDBRestoreFiles is the layout the drill runs against the scratch
// container.
func drillFDBRestoreFiles() fdbRestoreFiles {
	return fdbRestoreFiles{
		Log:     fdbRestoreLogPath,
		Exit:    fdbRestoreExitPath,
		Partial: fdbRestoreExitTemp,
	}
}

// fdbRestoreLaunchScript backgrounds whatever vector it is handed, sends that
// vector's output to the log, and publishes its exit status. The vector arrives
// as the shell's positional parameters and runs through "$@", so no value the
// drill did not write is ever parsed as shell program text; the three file
// names are the drill's own constants and are quoted as single words anyway.
//
// Clearing the previous run's files is a guard rather than a courtesy, so it
// carries `|| exit 1`. The status file is what tells the watch a restore
// finished, and the log is what an error quotes; a clear that failed and was
// stepped over would leave the last run's answers in place for this run's watch
// to read as its own, which is a restore reporting an outcome it never
// produced. The whole program cannot run under `set -e` instead: the group
// below is where a failing restore's status is published, and -e would exit the
// shell on that failure before `echo $?` ever ran.
//
// The shell is here for the backgrounding alone, and every part of it is a
// shell feature no engine CLI offers. `fdbrestore --waitfordone` runs for as
// long as the dataset needs while the exec that starts it has to return so the
// watch can poll, so the group's output is redirected to a file, which releases
// the exec's streams, and the group is put in the background with `&`. `echo $?`
// into a temp name followed by a move is what publishes a whole exit status
// rather than a half-written one. None of that is the command, and the command
// is the only thing carrying a value from outside.
func fdbRestoreLaunchScript(files fdbRestoreFiles) string {
	log, exit, partial := shellQuote(files.Log), shellQuote(files.Exit), shellQuote(files.Partial)
	return "rm -f " + log + " " + exit + " " + partial + " || exit 1; " +
		`{ "$@"; echo $? >` + partial + "; " +
		"mv " + partial + " " + exit + "; } >" + log + " 2>&1 &"
}

// fdbRestoreLaunchArgv is what the drill execs: the launch program, the name
// the shell reports as $0, and then the restore's own vector, which the program
// expands as "$@".
func fdbRestoreLaunchArgv(files fdbRestoreFiles, restore []string) []string {
	argv := []string{"sh", "-c", fdbRestoreLaunchScript(files), "fdbrestore-launch"}
	return append(argv, restore...)
}

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
	argv := fdbRestoreLaunchArgv(drillFDBRestoreFiles(), restore)
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
