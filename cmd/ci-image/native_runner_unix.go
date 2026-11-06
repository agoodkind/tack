//go:build linux || darwin

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func handleBrokenPipe() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGPIPE)
	return func() { signal.Stop(signals) }
}

func readRunnerMachine() (string, error) {
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		return "", &os.SyscallError{Syscall: "read runner machine with uname", Err: err}
	}
	return unix.ByteSliceToString(info.Machine[:]), nil
}
