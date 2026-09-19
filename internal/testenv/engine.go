package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// engineSpec describes one engine container.
type engineSpec struct {
	// kind names the engine in the container name.
	kind  string
	image string
	// platform pins the image variant; nil takes the daemon's own.
	platform *ocispec.Platform
	cmd      []string
	env      []string
	// files are written into the created container, path to contents, before
	// it starts, for an engine that reads its configuration from a file.
	files map[string][]byte `exhaustruct:"optional"`
	// entrypoint replaces the image's entrypoint when set.
	entrypoint []string `exhaustruct:"optional"`
	// networkOf names a container whose network stack the engine shares, so
	// the engine's address outlives the engine when it stops. Empty joins the
	// engines' network directly.
	networkOf string `exhaustruct:"optional"`
}

// engine is a started engine container.
type engine struct {
	name    string
	address string
}

// startEngine pulls spec's image when absent, then creates and starts a
// container that belongs to this process alone and records it for Release.
func startEngine(ctx context.Context, cli *client.Client, spec engineSpec) (engine, error) {
	if err := ensureNetwork(ctx, cli); err != nil {
		return engine{}, err
	}
	if err := joinNetworkWhenContainerized(ctx, cli); err != nil {
		return engine{}, err
	}
	if err := ensureImage(ctx, cli, spec); err != nil {
		return engine{}, err
	}
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return engine{}, err
	}
	name := "tack-testenv-" + spec.kind + "-" + strconv.Itoa(os.Getpid()) + "-" + suffix
	hostConfig := &container.HostConfig{}
	networking := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
	}
	if spec.networkOf != "" {
		hostConfig.NetworkMode = container.NetworkMode("container:" + spec.networkOf)
		networking = &network.NetworkingConfig{EndpointsConfig: nil}
	}
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      spec.image,
			Entrypoint: spec.entrypoint,
			Cmd:        spec.cmd,
			Env:        spec.env,
			Labels:     map[string]string{managedLabel: "true"},
		},
		HostConfig:       hostConfig,
		NetworkingConfig: networking,
		Platform:         spec.platform,
		Name:             name,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.create_failed", slog.String("err", err.Error()))
		return engine{}, fmt.Errorf("create container %s from %s: %w", name, spec.image, err)
	}
	own(name)
	for path, contents := range spec.files {
		if err := writeContainerFile(ctx, cli, name, path, contents); err != nil {
			return engine{}, err
		}
	}
	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.engine.start_failed", slog.String("err", err.Error()))
		return engine{}, fmt.Errorf("start container %s: %w", name, err)
	}
	address, err := containerAddress(ctx, cli, name)
	if err != nil {
		return engine{}, err
	}
	slog.InfoContext(ctx, "testenv.engine.started", slog.String("container", name), slog.String("image", spec.image))
	return engine{name: name, address: address}, nil
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
