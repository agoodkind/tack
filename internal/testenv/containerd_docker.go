package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const (
	// dindImage is the docker:29-dind index read from Docker Hub on 2026-10-02.
	dindImage = "docker:29-dind@sha256:7dcdfc4a20246236f558175182ccace1eb15a41bd3eb119dd2284f393498b7c1"
	// dindPort is the plain TCP port the daemon listens on when TLS is off.
	dindPort = "2375"
	// dindConfigPath is the daemon configuration file the dockerd reads.
	dindConfigPath = "/etc/docker/daemon.json"
	// dindContainerdConfig turns on the containerd image store.
	dindContainerdConfig = `{"features":{"containerd-snapshotter":true}}`
	// dindClassicConfig turns off the containerd image store, which selects the
	// classic overlay2 store.
	dindClassicConfig = `{"features":{"containerd-snapshotter":false}}`
	// dindReadyTimeout bounds the wait for the nested daemon to answer.
	dindReadyTimeout = 2 * time.Minute
	// dindRemoveTimeout bounds the removal of the nested daemon's container.
	dindRemoveTimeout = time.Minute
)

// ContainerdDocker starts a disposable privileged docker:dind container with
// the containerd image store enabled, waits until the daemon answers, and
// returns its TCP endpoint with a client for it. The container and its
// volumes are removed when the test ends, whether it passes or fails.
func ContainerdDocker(t *testing.T) (string, *client.Client) {
	t.Helper()
	return startDind(t, dindContainerdConfig)
}

// ClassicDocker starts a disposable privileged docker:dind container with the
// containerd image store disabled. The daemon uses the classic overlay2 store.
// It waits until the daemon answers and returns its TCP endpoint with a client
// for it. The container and its volumes are removed when the test ends.
func ClassicDocker(t *testing.T) (string, *client.Client) {
	t.Helper()
	return startDind(t, dindClassicConfig)
}

// startDind starts the pinned privileged docker:dind container with daemonConfig
// as its daemon.json and waits until the daemon answers.
func startDind(t *testing.T, daemonConfig string) (string, *client.Client) {
	t.Helper()
	ctx := t.Context()
	host, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("connect to the host Docker daemon: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	started, err := startEngine(ctx, host, engineSpec{
		kind:       "dind",
		image:      dindImage,
		platform:   nil,
		cmd:        nil,
		env:        []string{"DOCKER_TLS_CERTDIR="},
		files:      map[string][]byte{dindConfigPath: []byte(daemonConfig)},
		privileged: true,
	})
	if err != nil {
		t.Fatalf("start the docker:dind container: %v", err)
	}
	t.Cleanup(func() { removeDindContainer(t, host, started.name) })
	endpoint := "tcp://" + started.address + ":" + dindPort
	nested, err := client.New(client.WithHost(endpoint))
	if err != nil {
		t.Fatalf("build a Docker client for %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = nested.Close() })
	waitForDaemon(t, nested, endpoint)
	return endpoint, nested
}

func waitForDaemon(t *testing.T, nested *client.Client, endpoint string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), dindReadyTimeout)
	defer cancel()
	for {
		_, err := nested.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true, ForceNegotiate: false})
		if err == nil {
			return
		}
		if !sleepOrDone(ctx) {
			t.Fatalf("the daemon at %s did not answer within %s: %v", endpoint, dindReadyTimeout, err)
		}
	}
}

// removeDindContainer removes the docker:dind container with its anonymous
// volumes, fails the test when a volume remains, and logs their names.
func removeDindContainer(t *testing.T, host *client.Client, name string) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), dindRemoveTimeout)
	defer cancel()
	inspected, err := host.ContainerInspect(cleanup, name, client.ContainerInspectOptions{Size: false})
	if err != nil {
		t.Errorf("inspect container %s before removal: %v", name, err)
		return
	}
	var volumes []string
	for _, mount := range inspected.Container.Mounts {
		if mount.Name != "" {
			volumes = append(volumes, mount.Name)
		}
	}
	options := client.ContainerRemoveOptions{Force: true, RemoveVolumes: true, RemoveLinks: false}
	if _, err := host.ContainerRemove(cleanup, name, options); err != nil {
		t.Errorf("remove container %s: %v", name, err)
		return
	}
	for _, volume := range volumes {
		if _, err := host.VolumeInspect(cleanup, volume, client.VolumeInspectOptions{}); err == nil {
			t.Errorf("volume %s of container %s remains after removal", volume, name)
		}
	}
	t.Logf("removed container %s with volumes %v", name, volumes)
}
