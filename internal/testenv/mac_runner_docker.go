package testenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"
)

type macRunnerDaemon struct {
	ID           string `json:"id"`
	Architecture string `json:"architecture"`
	Version      string `json:"version"`
	APIVersion   string `json:"api_version"`
	Endpoint     string `json:"endpoint"`
}

func (execution *macRunnerExecution) connect(ctx context.Context) error {
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("require an absolute HOME for local Docker Desktop sockets")
	}
	var err error
	execution.defaultClient, err = client.New(client.WithHost("unix://" + filepath.Join(home, ".docker/run/docker.sock")))
	if err != nil {
		return macRunnerFailure("create local inspection client", err)
	}
	execution.rawClient, err = client.New(client.WithHost("unix://" + filepath.Join(home, "Library/Containers/com.docker.docker/Data/docker.raw.sock")))
	if err != nil {
		return macRunnerFailure("create local raw client", err)
	}
	standard, err := execution.daemon(ctx, execution.defaultClient, "default-daemon.json")
	if err != nil {
		return err
	}
	raw, err := execution.daemon(ctx, execution.rawClient, "raw-daemon.json")
	if err != nil {
		return err
	}
	if standard.ID != raw.ID || standard.Architecture != raw.Architecture || standard.Version != raw.Version {
		return errors.New("local inspection and raw sockets identify different Docker daemons")
	}
	image, err := macObserve(ctx, func(observation context.Context) (client.ImageInspectResult, error) {
		return execution.defaultClient.ImageInspect(observation, execution.prepared.options.Image)
	})
	if err != nil {
		return macRunnerFailure("inspect required immutable local image "+execution.prepared.options.Image, err)
	}
	writeMacRunnerJSON(execution, "pinned-image.json", macRunnerPinnedImage{Image: image, DaemonArchitecture: standard.Architecture})
	if image.ID != execution.prepared.options.Image {
		return fmt.Errorf("local image ID %s differs from required %s", image.ID, execution.prepared.options.Image)
	}
	if image.Config != nil {
		execution.imageEnvironment = image.Config.Env
	}
	writeMacRunnerJSON(execution, "runner-environment.json", macRunnerEnvironment{ImageDefaults: execution.imageEnvironment, PreparedOverrides: execution.request().Config.Env})
	return macRunnerErrors("record daemon evidence", execution.evidenceErrors...)
}

func (execution *macRunnerExecution) daemon(ctx context.Context, docker *client.Client, artifact string) (macRunnerDaemon, error) {
	_, err := macObserve(ctx, func(observation context.Context) (client.PingResult, error) {
		return docker.Ping(observation, client.PingOptions{NegotiateAPIVersion: true})
	})
	if err != nil {
		return macRunnerDaemon{}, macRunnerFailure("negotiate Docker endpoint "+docker.DaemonHost(), err)
	}
	info, err := macObserve(ctx, func(observation context.Context) (client.SystemInfoResult, error) {
		return docker.Info(observation, client.InfoOptions{})
	})
	if err != nil {
		return macRunnerDaemon{}, macRunnerFailure("inspect Docker endpoint "+docker.DaemonHost(), err)
	}
	version, err := macObserve(ctx, func(observation context.Context) (client.ServerVersionResult, error) {
		return docker.ServerVersion(observation, client.ServerVersionOptions{})
	})
	if err != nil {
		return macRunnerDaemon{}, macRunnerFailure("read Docker version "+docker.DaemonHost(), err)
	}
	identity := macRunnerDaemon{
		ID: info.Info.ID, Architecture: version.Arch, Version: version.Version,
		APIVersion: docker.ClientVersion(), Endpoint: docker.DaemonHost(),
	}
	writeMacRunnerJSON(execution, artifact, identity)
	if identity.ID == "" || identity.Architecture == "" || identity.Version == "" {
		return identity, errors.New("local Docker daemon did not report its ID, architecture and version")
	}
	return identity, nil
}
