package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// engineSpec describes one long-lived engine container.
type engineSpec struct {
	// name is fixed per engine, image, and command, so every test binary on
	// this daemon finds and reuses the same container.
	name  string
	image string
	// platform pins the image variant; nil takes the daemon's own.
	platform *ocispec.Platform
	cmd      []string
	// env builds the container environment from the credential the engine is
	// created with.
	env func(credential string) []string
}

// engine is a running engine container and the superuser credential it
// started with.
type engine struct {
	address      string
	superuserKey string
}

// startAttempts bounds how often ensureEngine replaces a container another
// process removed or left stopped between two of its calls.
const startAttempts = 3

// ensureEngine returns the running engine for spec, creating it when absent.
// An engine that stopped is removed and created again rather than restarted,
// because a restart can move its address, and both engines persist their own
// address.
func ensureEngine(ctx context.Context, cli *client.Client, spec engineSpec) (engine, error) {
	if err := ensureNetwork(ctx, cli); err != nil {
		return engine{}, err
	}
	if err := joinNetworkWhenContainerized(ctx, cli); err != nil {
		return engine{}, err
	}
	var lastErr error
	for range startAttempts {
		running, err := runningEngine(ctx, cli, spec)
		if err == nil {
			return running, nil
		}
		lastErr = err
	}
	return engine{}, lastErr
}

// runningEngine makes one attempt to find, start, or create spec's container.
func runningEngine(ctx context.Context, cli *client.Client, spec engineSpec) (engine, error) {
	found, err := cli.ContainerInspect(ctx, spec.name, client.ContainerInspectOptions{Size: false})
	switch {
	case cerrdefs.IsNotFound(err):
		if err := createEngine(ctx, cli, spec); err != nil {
			return engine{}, err
		}
	case err != nil:
		slog.ErrorContext(ctx, "testenv.engine.inspect_failed", slog.String("err", err.Error()))
		return engine{}, fmt.Errorf("inspect container %s: %w", spec.name, err)
	case found.Container.State != nil && found.Container.State.Status == container.StateRunning:
		return describeEngine(found.Container)
	case found.Container.State != nil && found.Container.State.Status == container.StateCreated:
		// Another process created it and has not started it yet; starting an
		// already started container is a no-op.
		if _, err := cli.ContainerStart(ctx, spec.name, client.ContainerStartOptions{}); err != nil {
			slog.ErrorContext(ctx, "testenv.engine.start_failed", slog.String("err", err.Error()))
			return engine{}, fmt.Errorf("start container %s: %w", spec.name, err)
		}
	default:
		_, _ = cli.ContainerRemove(ctx, spec.name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		return engine{}, fmt.Errorf("container %s had stopped and was removed", spec.name)
	}
	started, err := cli.ContainerInspect(ctx, spec.name, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.inspect_failed", slog.String("err", err.Error()))
		return engine{}, fmt.Errorf("inspect container %s after start: %w", spec.name, err)
	}
	return describeEngine(started.Container)
}

// describeEngine reads an engine's address and credential from its container.
func describeEngine(inspected container.InspectResponse) (engine, error) {
	address, err := engineAddress(inspected)
	if err != nil {
		return engine{}, err
	}
	superuserKey := ""
	if inspected.Config != nil {
		superuserKey = inspected.Config.Labels[credentialLabel]
	}
	return engine{address: address, superuserKey: superuserKey}, nil
}

// createEngine pulls spec's image when absent, then creates and starts the
// container with a fresh credential. Losing the create race to another
// process is not an error: the caller inspects the winner's container.
func createEngine(ctx context.Context, cli *client.Client, spec engineSpec) error {
	if err := ensureImage(ctx, cli, spec); err != nil {
		return err
	}
	credential, err := randomHex(ctx, 16)
	if err != nil {
		return err
	}
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  spec.image,
			Cmd:    spec.cmd,
			Env:    spec.env(credential),
			Labels: map[string]string{managedLabel: "true", credentialLabel: credential},
		},
		HostConfig: &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
		},
		Platform: spec.platform,
		Name:     spec.name,
	})
	if cerrdefs.IsConflict(err) || cerrdefs.IsAlreadyExists(err) {
		return nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create container %s from %s: %w", spec.name, spec.image, err)
	}
	if _, err := cli.ContainerStart(ctx, spec.name, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.engine.start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("start container %s: %w", spec.name, err)
	}
	slog.InfoContext(ctx, "testenv.engine.created", slog.String("container", spec.name), slog.String("image", spec.image))
	return nil
}

// ensureImage pulls spec's image unless the daemon already holds it for the
// wanted platform.
func ensureImage(ctx context.Context, cli *client.Client, spec engineSpec) error {
	inspectOptions := []client.ImageInspectOption{}
	pullPlatforms := []ocispec.Platform{}
	if spec.platform != nil {
		inspectOptions = append(inspectOptions, client.ImageInspectWithPlatform(spec.platform))
		pullPlatforms = append(pullPlatforms, *spec.platform)
	}
	if _, err := cli.ImageInspect(ctx, spec.image, inspectOptions...); err == nil {
		return nil
	}
	pulled, err := cli.ImagePull(ctx, spec.image, client.ImagePullOptions{
		All: false, RegistryAuth: "", PrivilegeFunc: nil, Platforms: pullPlatforms,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.image.pull_failed", slog.String("err", err.Error()))
		return fmt.Errorf("pull image %s: %w", spec.image, err)
	}
	defer func() { _ = pulled.Close() }()
	if err := pulled.Wait(ctx); err != nil {
		slog.ErrorContext(ctx, "testenv.image.pull_failed", slog.String("err", err.Error()))
		return fmt.Errorf("pull image %s: %w", spec.image, err)
	}
	return nil
}

// pollInterval spaces readiness probes against a starting engine.
const pollInterval = 2 * time.Second

// sleepOrDone waits one poll interval and reports whether ctx is still live.
func sleepOrDone(ctx context.Context) bool {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
