package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// TofuProcess is a command that runs in the runner container while the test
// continues. Wait returns its output and exit code after the command ends.
type TofuProcess struct {
	finished chan struct{}
	output   string
	code     int
	err      error
}

// Start runs command asynchronously. The test must call Wait on the returned process.
func (host OpenTofuHost) Start(t T, environment []string, command ...string) *TofuProcess {
	t.Helper()
	process := &TofuProcess{finished: make(chan struct{}), output: "", code: 0, err: nil}
	ctx, cancel := context.WithTimeout(t.Context(), tofuCommandTimeout)
	cli, err := dockerClient(ctx)
	if err != nil {
		cancel()
		failWith(t, err)
	}
	go func() {
		defer close(process.finished)
		defer cancel()
		defer func() { _ = cli.Close() }()
		defer func() {
			if recovered := recover(); recovered != nil {
				process.err = fmt.Errorf("asynchronous OpenTofu command panicked:\n%s", debug.Stack())
				slog.ErrorContext(ctx, "testenv.tofu.start_panicked", slog.Any("err", process.err))
			}
		}()

		process.output, process.code, process.err = execInContainerWithEnv(ctx, cli, host.name, command, environment)
	}()
	return process
}

// Wait blocks until the command ends and returns its combined output and exit
// code. The test fails when the command could not run.
func (process *TofuProcess) Wait(t T) (string, int) {
	t.Helper()
	<-process.finished
	if process.err != nil {
		failWith(t, process.err)
	}
	return process.output, process.code
}
