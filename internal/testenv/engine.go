package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// processLabel records the test process that created an engine container.
// The Mac runner attributes a fixed-name engine to its test process by this
// label and the engine's testenv.engine.started log line.
const processLabel = managedLabel + ".process"

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
	// name replaces the generated container name, for an engine that the
	// code under test finds by a fixed name. Empty generates a name unique to
	// this process.
	name string `exhaustruct:"optional"`
	// healthcheck is the container's health check; nil keeps the image's.
	healthcheck *container.HealthConfig `exhaustruct:"optional"`
	// attachNetwork replaces the engines' network as the one network the
	// engine joins. Empty joins the engines' network.
	attachNetwork string `exhaustruct:"optional"`
	// ipv4Address fixes the engine's address on its network. The zero value
	// takes an address the daemon allocates.
	ipv4Address netip.Addr `exhaustruct:"optional"`
	// extraHosts are name:address entries the daemon adds to the engine's
	// /etc/hosts.
	extraHosts []string `exhaustruct:"optional"`
	// privileged runs the engine with every device and capability, for an
	// engine that runs its own container daemon.
	privileged bool `exhaustruct:"optional"`
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
	name, err := engineName(ctx, spec)
	if err != nil {
		return engine{}, err
	}
	hostConfig, networking := engineNetworking(spec)
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:       spec.image,
			Hostname:    name,
			Entrypoint:  spec.entrypoint,
			Cmd:         spec.cmd,
			Env:         spec.env,
			Healthcheck: spec.healthcheck,
			Labels:      map[string]string{managedLabel: "true", processLabel: strconv.Itoa(os.Getpid())},
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
	address, err := containerAddressOn(ctx, cli, name, engineNetwork(spec))
	if err != nil {
		return engine{}, engineStartFailure(ctx, cli, name, err)
	}
	slog.InfoContext(ctx, "testenv.engine.started", slog.String("container", name), slog.String("image", spec.image))
	return engine{name: name, address: address}, nil
}

// engineName returns spec's fixed name when it sets one, and otherwise a name
// that belongs to this process alone.
func engineName(ctx context.Context, spec engineSpec) (string, error) {
	if spec.name != "" {
		return spec.name, nil
	}
	return generatedEngineName(ctx, spec.kind)
}

// generatedEngineName returns a container name for kind that belongs to this
// process alone, in the tack-testenv-<kind>-<pid>-<8 hex> form.
func generatedEngineName(ctx context.Context, kind string) (string, error) {
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return "", err
	}
	return "tack-testenv-" + kind + "-" + strconv.Itoa(os.Getpid()) + "-" + suffix, nil
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
