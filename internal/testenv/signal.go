package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
)

// signalReleaseTimeout bounds cleanup of this process's engines after a signal.
const signalReleaseTimeout = 2 * time.Minute

// releaseOnSignal installs a handler that removes this process's engines on
// SIGINT or SIGTERM, restores default handling, and sends the same signal to
// this process again. The process then ends with that signal.
func releaseOnSignal(ctx context.Context) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGINT, unix.SIGTERM)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "testenv.signal.release_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		received := <-signals
		releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), signalReleaseTimeout)
		defer cancel()
		slog.InfoContext(releaseContext, "testenv.signal.received", slog.String("signal", received.String()))
		_ = Release(releaseContext)
		signal.Reset(received)
		if number, isSignal := received.(unix.Signal); isSignal {
			_ = unix.Kill(os.Getpid(), number)
		}
	}()
}
