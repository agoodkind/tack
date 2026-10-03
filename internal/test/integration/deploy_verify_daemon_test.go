package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/ops"
)

const (
	// deployRegistryImage serves the registry HTTP API for the test. The
	// digest is the Docker Hub index of registry:3 read on 2026-10-02.
	deployRegistryImage = "registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
	// deployTestenvLabel marks containers testenv removes on release.
	deployTestenvLabel = "io.goodkind.tack.testenv"
)

// hasContainerdImageStore logs the daemon driver and DriverStatus and reports
// whether DriverStatus has the containerd image store driver-type entry.
func hasContainerdImageStore(t *testing.T, cli *client.Client) bool {
	t.Helper()
	info, err := cli.Info(t.Context(), client.InfoOptions{})
	if err != nil {
		t.Fatalf("read docker info: %v", err)
	}
	t.Logf("daemon driver=%s driverStatus=%v", info.Info.Driver, info.Info.DriverStatus)
	for _, entry := range info.Info.DriverStatus {
		if entry[0] == ops.ImageStoreDriverTypeKey && entry[1] == ops.ContainerdSnapshotterDriverType {
			return true
		}
	}
	return false
}

// requireContainerdImageStore fails the test unless the daemon reports the
// containerd image store driver-type in docker info.
func requireContainerdImageStore(t *testing.T, cli *client.Client) {
	t.Helper()
	if !hasContainerdImageStore(t, cli) {
		t.Fatalf("daemon has no %s = %s entry", ops.ImageStoreDriverTypeKey, ops.ContainerdSnapshotterDriverType)
	}
}

// requireClassicImageStore fails the test when the daemon reports the
// containerd image store driver-type in docker info.
func requireClassicImageStore(t *testing.T, cli *client.Client) {
	t.Helper()
	if hasContainerdImageStore(t, cli) {
		t.Fatalf("daemon has a %s = %s entry, want the classic image store", ops.ImageStoreDriverTypeKey, ops.ContainerdSnapshotterDriverType)
	}
}

// startDeployRegistry starts a registry inside the daemon behind cli with its
// API port published on the daemon's own address, daemonAddress. It returns a
// registry client for the test process and the published port the daemon
// pulls from on localhost.
func startDeployRegistry(t *testing.T, cli *client.Client, daemonAddress string) (localRegistry, string) {
	t.Helper()
	pullImage(t, cli, deployRegistryImage)
	name := "tack-testenv-registry-" + uuid.NewString()[:8]
	apiPort := network.MustParsePort("5000/tcp")
	_, err := cli.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config: &container.Config{
			Image: deployRegistryImage, ExposedPorts: network.PortSet{apiPort: {}},
			Labels: map[string]string{deployTestenvLabel: "true"},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{apiPort: {{HostPort: ""}}}},
		Name:       name,
	})
	if err != nil {
		t.Fatalf("create the registry: %v", err)
	}
	t.Cleanup(func() { removeDeployContainer(t, cli, name) })
	if _, err := cli.ContainerStart(t.Context(), name, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start the registry: %v", err)
	}
	inspected, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.NetworkSettings == nil || len(inspected.Container.NetworkSettings.Ports[apiPort]) == 0 {
		t.Fatalf("read the registry port: %v", err)
	}
	hostPort := inspected.Container.NetworkSettings.Ports[apiPort][0].HostPort
	registry := localRegistry{base: "http://" + daemonAddress + ":" + hostPort, http: &http.Client{Timeout: 30 * time.Second}}
	waitForRegistry(t, registry)
	return registry, hostPort
}

func waitForRegistry(t *testing.T, registry localRegistry) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, registry.base+"/v2/", nil)
		if err != nil {
			t.Fatalf("build the registry probe: %v", err)
		}
		if response, err := registry.http.Do(request); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the %s did not answer /v2/ within a minute", registry.base)
}
