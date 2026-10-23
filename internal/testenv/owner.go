package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

const (
	// runLabel stores an identifier unique to the creating process. Release
	// removes every container and network with this value, including containers
	// that a failed or canceled Docker call created before this process recorded
	// them.
	runLabel = managedLabel + ".run"
	// hostLabel records the host name of the creating process.
	hostLabel = managedLabel + ".host"
	// runnerLabel records the container ID of the creating process's runner.
	// Its value is empty when the process runs on the Docker host.
	runnerLabel = managedLabel + ".runner"
	// operatorLabel marks an engine that cmd/testenv started for an operator.
	// Test processes keep it after the tool exits; testenv down removes it.
	operatorLabel = managedLabel + ".operator"
)

// processOwner records the identity of the process that creates engines,
// as its owner labels record it.
type processOwner struct {
	run    string
	host   string
	runner string
}

var (
	// ownerState stores this process's identity after the first lookup.
	ownerState struct {
		sync.Mutex
		owner processOwner
	}
	// testProcessPrepared runs preparation once per test binary.
	testProcessPrepared sync.Once
)

// currentOwner looks up this process's identity on the first call and
// returns the same identity on later calls.
func currentOwner(ctx context.Context, cli *client.Client) (processOwner, error) {
	ownerState.Lock()
	defer ownerState.Unlock()
	if ownerState.owner.run != "" {
		return ownerState.owner, nil
	}
	run, err := randomHex(ctx, 8)
	if err != nil {
		return processOwner{}, err
	}
	host, err := os.Hostname()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.hostname_failed", slog.String("err", err.Error()))
		return processOwner{}, fmt.Errorf("read this host's name: %w", err)
	}
	runner, err := selfContainer(ctx, cli)
	if err != nil {
		return processOwner{}, err
	}
	ownerState.owner = processOwner{run: run, host: host, runner: runner}
	return ownerState.owner, nil
}

// knownOwner returns this process's identity and whether an earlier call
// looked it up. A false result means this process has not created a
// container or network.
func knownOwner() (processOwner, bool) {
	ownerState.Lock()
	defer ownerState.Unlock()
	return ownerState.owner, ownerState.owner.run != ""
}

// ownerLabels returns labels for a container or network this process creates.
// In a test binary, the first call installs a handler that removes this
// process's engines on SIGINT or SIGTERM and removes the engines of test
// processes that have exited.
func ownerLabels(ctx context.Context, cli *client.Client) (map[string]string, error) {
	owner, err := currentOwner(ctx, cli)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{
		managedLabel: "true",
		processLabel: strconv.Itoa(os.Getpid()),
		runLabel:     owner.run,
		hostLabel:    owner.host,
		runnerLabel:  owner.runner,
	}
	if !testing.Testing() {
		labels[operatorLabel] = "true"
		return labels, nil
	}
	testProcessPrepared.Do(func() {
		releaseOnSignal(ctx)
		_, _ = removeEngines(ctx, cli, owner, ownerExited)
	})
	return labels, nil
}

// ownerExited reports whether the test process that created a container or
// network has exited. It returns false for operator engines, missing owner
// labels, or owners it cannot check. It checks whether a different runner
// container is gone or stopped, or checks the process ID when the owner
// shares this process's host and runner. It cannot check another host's
// process ID or a Docker host's process ID from a container.
func ownerExited(ctx context.Context, cli *client.Client, self processOwner, labels map[string]string) (bool, error) {
	host, labeled := labels[hostLabel]
	process, numbered := labeledProcess(labels)
	if !labeled || !numbered || labels[operatorLabel] == "true" {
		return false, nil
	}
	runner := labels[runnerLabel]
	switch {
	case runner != "" && runner != self.runner:
		return runnerStopped(ctx, cli, runner)
	case runner == self.runner && host == self.host:
		return !processRuns(process), nil
	default:
		return false, nil
	}
}

// labeledProcess returns the process ID from labels[processLabel]. It returns
// false if the label is missing or is not a number.
func labeledProcess(labels map[string]string) (int, bool) {
	process, err := strconv.Atoi(labels[processLabel])
	if err != nil {
		return 0, false
	}
	return process, true
}

// runnerStopped reports whether the container with ID runner is gone or
// stopped. A stopped container runs none of its processes.
func runnerStopped(ctx context.Context, cli *client.Client, runner string) (bool, error) {
	inspected, err := cli.ContainerInspect(ctx, runner, client.ContainerInspectOptions{Size: false})
	if cerrdefs.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.inspect_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("inspect runner container %s: %w", runner, err)
	}
	state := inspected.Container.State
	return state != nil && !state.Running, nil
}

// processRuns reports whether process exists in this process's PID namespace.
// Signal 0 checks without sending a signal; EPERM means the process exists
// under another user.
func processRuns(process int) bool {
	err := unix.Kill(process, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
