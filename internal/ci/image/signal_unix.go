//go:build linux || darwin

package image

import (
	"os"
	"os/signal"
	"syscall"
)

// handleBrokenPipe registers SIGPIPE notification so writes to closed
// standard output return an error. It returns a function that stops
// the notification.
func handleBrokenPipe() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGPIPE)

	return func() { signal.Stop(signals) }
}
