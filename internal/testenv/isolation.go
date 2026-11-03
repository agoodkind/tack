package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// isolation stores the network that an isolated run uses. The zero value keeps
// the shared network and the startup sweep of exited test processes.
var isolation struct {
	sync.Mutex
	network string
	project string
}

const (
	// composeProjectLabel, composeServiceLabel, and composeOneOffLabel are the
	// labels that Compose assigns to a project's containers. An isolated run
	// gives its engines these labels. Compose down does not remove the engines,
	// because Compose finds a project's containers by the config-hash label that
	// the SDK does not set. `make test-postgresql-overlay` selects the engines
	// by the project and service labels and removes them before the final
	// Compose down, even when the process that started them cannot run
	// [Release].
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
	composeOneOffLabel  = "com.docker.compose.oneoff"
	// isolatedServiceName is the service name that isolated engines record. The
	// Compose file declares no such service.
	isolatedServiceName = "testenv-engine"
)

// IsolateRun makes this process attach its engines to network and skip the
// startup sweep of other test processes' engines. The network must exist and
// must already include this process's runner container, as the network of the
// Compose project that runs the tests does. The engines also record project as
// their Compose project label, which the caller's cleanup selects by. [Release]
// removes this process's engines through their run label and never creates,
// labels, or removes the network.
// IsolateRun fails after this process has started an engine.
func IsolateRun(network, project string) error {
	if network == "" || project == "" {
		return errors.New("an isolated run needs the names of an existing Docker network and its Compose project")
	}
	if _, started := knownOwner(); started {
		return errors.New("IsolateRun must run before this process starts an engine or network")
	}
	isolation.Lock()
	defer isolation.Unlock()
	isolation.network = network
	isolation.project = project
	return nil
}

// isolationLabels adds the Compose project labels to labels when IsolateRun
// applies to this process, and returns labels.
func isolationLabels(labels map[string]string) map[string]string {
	isolation.Lock()
	defer isolation.Unlock()
	if isolation.project != "" {
		labels[composeProjectLabel] = isolation.project
		labels[composeServiceLabel] = isolatedServiceName
		labels[composeOneOffLabel] = "False"
	}
	return labels
}

// activeNetwork returns the network that engines join.
func activeNetwork() string {
	isolation.Lock()
	defer isolation.Unlock()
	if isolation.network != "" {
		return isolation.network
	}
	return networkName
}

// isolated reports whether IsolateRun applies to this process.
func isolated() bool {
	isolation.Lock()
	defer isolation.Unlock()
	return isolation.network != ""
}

// requireIsolatedNetwork fails unless the isolated network exists.
func requireIsolatedNetwork(ctx context.Context, cli *client.Client) error {
	name := activeNetwork()
	if _, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{Scope: "", Verbose: false}); err != nil {
		slog.ErrorContext(ctx, "testenv.network.isolated_missing", slog.String("err", err.Error()))
		return fmt.Errorf("the isolated network %s does not exist: %w", name, err)
	}
	return nil
}

const (
	// decoyNeverRunner is the runner ID that a decoy records. No container has it.
	decoyNeverRunner = "0000000000000000000000000000000000000000000000000000000000000000"
	// decoyNeverProcess is the process ID that a decoy records.
	decoyNeverProcess = "999999"
)

// CreateStoppedRunDecoy creates a container that is not running. It has this
// process's run label, and [Release] and the signal handler remove it. Its
// runner and process labels name a runner container and a process that do not
// exist. An ordinary startup sweep of another process removes such a
// container. The decoy uses the image of the container that runs this process
// and never starts.
func CreateStoppedRunDecoy(ctx context.Context) (string, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	runner, err := selfContainer(ctx, cli)
	if err != nil {
		return "", err
	}
	if runner == "" {
		return "", errors.New("a decoy needs the image of the runner container, and this process does not run in a container")
	}
	self, err := cli.ContainerInspect(ctx, runner, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.decoy.runner_inspect_failed", slog.String("container", runner), slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect runner container %s: %w", runner, err)
	}
	name, err := generatedEngineName(ctx, "decoy")
	if err != nil {
		return "", err
	}
	owner, err := prepareCreate(ctx, cli)
	if err != nil {
		return "", err
	}
	labels := isolationLabels(ownerLabels(owner))
	labels[runnerLabel] = decoyNeverRunner
	labels[processLabel] = decoyNeverProcess
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: self.Container.Config.Image, Labels: labels},
		Name:   name,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.decoy.create_failed", slog.String("container", name), slog.String("err", err.Error()))
		return "", fmt.Errorf("create decoy container %s: %w", name, err)
	}
	return name, nil
}

// StoppedRunDecoyExists reports whether the decoy container still exists.
func StoppedRunDecoyExists(ctx context.Context, name string) (bool, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = cli.Close() }()
	_, err = cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{Size: false})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.decoy.inspect_failed", slog.String("container", name), slog.String("err", err.Error()))
		return false, fmt.Errorf("inspect decoy container %s: %w", name, err)
	}
	return true, nil
}

// RemoveStoppedRunDecoy removes the decoy container. A container already gone
// counts as removed.
func RemoveStoppedRunDecoy(ctx context.Context, name string) error {
	return removeContainers(ctx, []string{name})
}
