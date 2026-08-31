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
