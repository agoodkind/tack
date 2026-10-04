package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	// networkName is the bridge every engine joins. The test process reaches
	// an engine at its address on this network: directly from a Linux host,
	// and by joining the network when the process itself runs in a container.
	networkName = "tack-testenv"
	// managedLabel marks the network and the containers this package owns, so
	// Down finds every engine whatever process started it.
	managedLabel = "io.goodkind.tack.testenv"
)

// dockerClient connects to the local daemon, pinned to the default socket so
// a DOCKER_HOST inherited from the environment never points a test at a
// remote daemon, and fails with a plain reason when it does not answer.
func dockerClient(ctx context.Context) (*client.Client, error) {
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		slog.ErrorContext(ctx, "testenv.docker.connect_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("build a Docker client for %s: %w", client.DefaultDockerHost, err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true, ForceNegotiate: false}); err != nil {
		_ = cli.Close()
		slog.ErrorContext(ctx, "testenv.docker.ping_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf(
			"the Docker daemon at %s did not answer, and store-backed tests need it (start Docker, or run go test -short to skip them): %w",
			client.DefaultDockerHost, err)
	}
	return cli, nil
}

// pingDocker is the provisioning step behind RequireDocker.
func pingDocker(ctx context.Context) (string, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	_ = cli.Close()
	return client.DefaultDockerHost, nil
}

// ensureNetwork creates the engines' bridge network unless it exists.
func ensureNetwork(ctx context.Context, cli *client.Client) error {
	_, err := cli.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{Scope: "", Verbose: false})
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		slog.ErrorContext(ctx, "testenv.network.inspect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("inspect network %s: %w", networkName, err)
	}
	// IPv4 only, whatever the daemon's default network options say: the
	// upstream images' entrypoints cannot announce an IPv6 address without the
	// production overlays, and these tests exercise the storage layer, not the
	// IPv6-only contract.
	enableIPv4, enableIPv6 := true, false
	_, err = cli.NetworkCreate(ctx, networkName, client.NetworkCreateOptions{
		Driver:     "bridge",
		EnableIPv4: &enableIPv4,
		EnableIPv6: &enableIPv6,
		Labels:     map[string]string{managedLabel: "true"},
	})
	if err != nil && !cerrdefs.IsConflict(err) && !cerrdefs.IsAlreadyExists(err) {
		slog.ErrorContext(ctx, "testenv.network.create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create network %s: %w", networkName, err)
	}
	return nil
}

// joinNetworkWhenContainerized attaches the container this process runs in to
// the engines' network, so engine addresses on that network are reachable.
func joinNetworkWhenContainerized(ctx context.Context, cli *client.Client) error {
	_, err := joinWhenContainerized(ctx, cli, networkName)
	return err
}

// joinWhenContainerized attaches the container this process runs in to
// target and returns that container's ID. Docker sets a container's hostname
// to its ID. When no container on this daemon has this process's hostname,
// the process runs on the host, and the function returns an empty ID.
func joinWhenContainerized(ctx context.Context, cli *client.Client, target string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.hostname_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read this host's name: %w", err)
	}
	self, err := cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{Size: false})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.self.inspect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect container %s: %w", hostname, err)
	}
	if self.Container.Config == nil || self.Container.Config.Hostname != hostname {
		return "", nil
	}
	if attachedTo(self.Container, target) {
		return self.Container.ID, nil
	}
	_, err = cli.NetworkConnect(ctx, target, client.NetworkConnectOptions{
		Container:      self.Container.ID,
		EndpointConfig: &network.EndpointSettings{},
	})
	if err == nil {
		return self.Container.ID, nil
	}
	// Another test binary in this container may have joined first.
	again, inspectErr := cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{Size: false})
	if inspectErr == nil && attachedTo(again.Container, target) {
		return self.Container.ID, nil
	}
	slog.ErrorContext(ctx, "testenv.self.join_failed", slog.String("err", err.Error()))
	return "", fmt.Errorf("attach container %s to network %s: %w", hostname, target, err)
}

// attachedTo reports whether a container is attached to target.
func attachedTo(inspected container.InspectResponse, target string) bool {
	if inspected.NetworkSettings == nil {
		return false
	}
	_, attached := inspected.NetworkSettings.Networks[target]
	return attached
}

// addressOn returns a container's IP address on target.
func addressOn(inspected container.InspectResponse, target string) (string, error) {
	if !attachedTo(inspected, target) {
		return "", fmt.Errorf("container %s is not attached to network %s", inspected.Name, target)
	}
	address := inspected.NetworkSettings.Networks[target].IPAddress
	if !address.IsValid() {
		return "", fmt.Errorf("container %s has no address on network %s", inspected.Name, target)
	}
	return address.String(), nil
}

// containerAddress returns a container's IP address on the engines' network.
func containerAddress(ctx context.Context, cli *client.Client, containerName string) (string, error) {
	return containerAddressOn(ctx, cli, containerName, networkName)
}

// containerAddressOn returns a container's IP address on target. For a
// container that shares another container's network stack, it reads the
// address of that other container.
func containerAddressOn(ctx context.Context, cli *client.Client, containerName, target string) (string, error) {
	inspected, err := cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.inspect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect container %s: %w", containerName, err)
	}
	hostConfig := inspected.Container.HostConfig
	if hostConfig != nil && hostConfig.NetworkMode.IsContainer() {
		return containerAddressOn(ctx, cli, hostConfig.NetworkMode.ConnectedContainer(), target)
	}
	return addressOn(inspected.Container, target)
}
