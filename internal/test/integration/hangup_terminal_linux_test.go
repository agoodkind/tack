package integration_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// hangupLogPoll sets the interval between log checks.
const hangupLogPoll = 200 * time.Millisecond

// terminalProcess tracks a command running with a controlling pseudo-terminal.
type terminalProcess struct {
	command *exec.Cmd
	master  *os.File
	output  *bytes.Buffer
	// exited signals that the wait goroutine has stored the [exec.Cmd.Wait]
	// error in waitErr.
	exited  chan struct{}
	waitErr error
}

// startInTerminal starts binary as a session leader with a controlling
// pseudo-terminal on standard input and captures standard output and error.
// Closing the master triggers the kernel SIGHUP path that sshd uses when
// an SSH session drops.
func startInTerminal(t *testing.T, binary string, environment []string, arguments ...string) *terminalProcess {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open the pseudo-terminal master: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock the pseudo-terminal: %v", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("read the pseudo-terminal number: %v", err)
	}
	terminal, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open the pseudo-terminal %d: %v", number, err)
	}
	output := &bytes.Buffer{}
	command := exec.Command(binary, arguments...)
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = terminal, output, output
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	startErr := command.Start()
	_ = terminal.Close()
	if startErr != nil {
		t.Fatalf("start %v in the pseudo-terminal: %v", arguments, startErr)
	}
	process := &terminalProcess{command: command, master: master, output: output, exited: make(chan struct{}), waitErr: nil}
	go func() {
		process.waitErr = command.Wait()
		close(process.exited)
	}()
	t.Cleanup(process.kill)
	return process
}

// kill ends the process if it is still running and waits for [exec.Cmd.Wait]
// to return during test cleanup.
func (process *terminalProcess) kill() {
	select {
	case <-process.exited:
	default:
		_ = process.command.Process.Kill()
		<-process.exited
	}
}

// hangUp closes the pseudo-terminal master to trigger SIGHUP.
func (process *terminalProcess) hangUp(t *testing.T) {
	t.Helper()
	if err := process.master.Close(); err != nil {
		t.Fatalf("close the pseudo-terminal master: %v", err)
	}
}

// waitForExit returns the process wait status or fails the test if the
// process does not exit within limit.
func (process *terminalProcess) waitForExit(t *testing.T, limit time.Duration) syscall.WaitStatus {
	t.Helper()
	select {
	case <-process.exited:
	case <-time.After(limit):
		t.Fatalf("the process did not exit within %s", limit)
	}
	status, ok := process.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("the process state %v does not contain a wait status", process.command.ProcessState)
	}
	return status
}

// waitForLogRecord polls the command log for the given JSON message name.
// It fails the test if the process exits first or the time limit passes.
func (process *terminalProcess) waitForLogRecord(t *testing.T, pattern, name string, limit time.Duration) {
	t.Helper()
	deadline := time.After(limit)
	ticker := time.NewTicker(hangupLogPoll)
	defer ticker.Stop()
	for !logContainsRecord(t, pattern, name) {
		select {
		case <-process.exited:
			t.Fatalf("the process exited with %v before it logged %s:\n%s", process.waitErr, name, process.output)
		case <-deadline:
			t.Fatalf("the process did not log %s within %s", name, limit)
		case <-ticker.C:
		}
	}
}

// logContainsRecord reports whether the single file matching pattern
// contains a JSON record with the given message name. It returns false
// unless exactly one file matches.
func logContainsRecord(t *testing.T, pattern, name string) bool {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("match the log files %s: %v", pattern, err)
	}
	if len(matches) != 1 {
		return false
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read the log file %s: %v", matches[0], err)
	}
	return bytes.Contains(contents, []byte(`"msg":"`+name+`"`))
}
