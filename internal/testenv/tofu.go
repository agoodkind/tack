package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"time"
)

// tofuCommandTimeout bounds one command run in an OpenTofu host.
const tofuCommandTimeout = 10 * time.Minute

// tofuDirectoryBytes is the random size of the working directory name.
const tofuDirectoryBytes = 8

// OpenTofuHost runs commands in the container that this test process runs in,
// through the Docker SDK. Directory is a new working directory inside that
// container. The runner image must contain the tofu executable and any provider
// the commands use, and the container is removed with the runner.
type OpenTofuHost struct {
	name string
	// Directory is an absolute path in the container that only this host uses.
	Directory string
}

// StartOpenTofuHost returns a host for the runner container. The test fails
// when the process does not run in a container.
func StartOpenTofuHost(t T) OpenTofuHost {
	t.Helper()
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	runner, err := RunnerContainer(ctx)
	if err != nil {
		failWith(t, err)
	}
	if runner == "" {
		failWith(t, errors.New("the test process does not run in a container, and OpenTofu commands run in the runner container"))
	}
	suffix, err := randomHex(ctx, tofuDirectoryBytes)
	if err != nil {
		failWith(t, err)
	}
	return OpenTofuHost{name: runner, Directory: path.Join("/tmp", "tack-tofu-"+suffix)}
}

// Run runs the command in the container with the KEY=VALUE entries in environment
// and returns the combined output and the exit code.
func (host OpenTofuHost) Run(t T, environment []string, command ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tofuCommandTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		failWith(t, err)
	}
	defer func() { _ = cli.Close() }()
	output, code, err := execInContainerWithEnv(ctx, cli, host.name, command, environment)
	if err != nil {
		failWith(t, err)
	}
	return output, code
}

// WriteFile writes contents to an absolute path in the container.
func (host OpenTofuHost) WriteFile(t T, filePath string, contents []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tofuCommandTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		failWith(t, err)
	}
	defer func() { _ = cli.Close() }()
	if err := writeContainerFile(ctx, cli, host.name, filePath, contents); err != nil {
		failWith(t, err)
	}
}

// ReadFile returns the contents of one regular file in the container.
func (host OpenTofuHost) ReadFile(t T, filePath string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tofuCommandTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		failWith(t, err)
	}
	defer func() { _ = cli.Close() }()
	contents, err := readContainerFile(ctx, cli, host.name, filePath)
	if err != nil {
		failWith(t, err)
	}
	return contents
}

// failWith writes err to the test output and stops the test.
func failWith(t T, err error) {
	t.Helper()
	slog.Error("testenv.tofu.failed", slog.String("err", err.Error()))
	_, _ = io.WriteString(t.Output(), fmt.Sprintf("testenv: %v\n", err))
	t.FailNow()
}
