package testenv

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"

	"golang.org/x/sys/unix"
)

// runWatcherVariable stores the run label that a watcher process removes.
// Only startRunWatcher sets this environment variable.
const runWatcherVariable = "TACK_TESTENV_WATCHED_RUN"

func init() {
	run := os.Getenv(runWatcherVariable)
	if run != "" {
		os.Exit(watchRun(run))
	}
}

// startRunWatcher starts this binary again as a watcher of run. When this
// process ends for any reason, the kernel closes the write end of the
// watcher's standard input. A goroutine waits for the watcher and retains
// the pipe until the watcher exits.
//
// The watcher shares this process's standard error stream. Before exiting,
// go test waits for that stream to close for one tenth of its timeout, with
// a minimum of 5 seconds.
func startRunWatcher(ctx context.Context, run string) error {
	executable, err := os.Executable()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.watcher.start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("find this test binary: %w", err)
	}
	command := exec.CommandContext(context.WithoutCancel(ctx), executable)
	command.Env = append(os.Environ(), runWatcherVariable+"="+run)
	command.Stderr = os.Stderr
	if _, err := command.StdinPipe(); err != nil {
		slog.ErrorContext(ctx, "testenv.watcher.start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create the pipe of the run watcher: %w", err)
	}
	if err := command.Start(); err != nil {
		slog.ErrorContext(ctx, "testenv.watcher.start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("start the run watcher: %w", err)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "testenv.watcher.wait_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		if err := command.Wait(); err != nil {
			slog.ErrorContext(ctx, "testenv.watcher.wait_failed", slog.String("err", err.Error()))
		}
	}()
	return nil
}

// watchRun waits until the test binary ends, removes every container and
// network with the run label, and returns the exit status. It ignores
// signals that a process group receives when a test run is canceled.
func watchRun(run string) int {
	signal.Ignore(unix.SIGINT, unix.SIGTERM, unix.SIGHUP)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		slog.Error("testenv.watcher.read_failed", slog.String("err", err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineReleaseTimeout)
	defer cancel()
	if err := removeRun(ctx, processOwner{run: run, host: "", runner: "", native: false, operator: false}); err != nil {
		slog.ErrorContext(ctx, "testenv.watcher.release_failed", slog.String("err", err.Error()))
		return 1
	}
	return 0
}
