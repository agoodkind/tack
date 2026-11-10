//go:build linux || darwin

package native

import (
	"os"

	"golang.org/x/sys/unix"
)

func readRunnerMachine() (string, error) {
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		return "", &os.SyscallError{Syscall: "read runner machine with uname", Err: err}
	}
	return unix.ByteSliceToString(info.Machine[:]), nil
}
