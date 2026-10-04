package integration

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func pullImage(t *testing.T, cli *client.Client, ref string) {
	t.Helper()
	response, err := cli.ImagePull(t.Context(), ref, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
	defer func() { _ = response.Close() }()
	if err := response.Wait(t.Context()); err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
}

// createFromIndex pulls ref and creates, without starting, a container named
// name from it. The image and the container are removed when the test ends.
func createFromIndex(t *testing.T, cli *client.Client, name, ref string) {
	t.Helper()
	pullImage(t, cli, ref)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := cli.ImageRemove(cleanup, ref, client.ImageRemoveOptions{Force: true}); err != nil {
			t.Errorf("remove image %s: %v", ref, err)
			return
		}
		t.Logf("removed image %s", ref)
	})
	_, err := cli.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config: &container.Config{Image: ref, Cmd: []string{"/marker"}, Labels: map[string]string{deployTestenvLabel: "true"}},
		Name:   name,
	})
	if err != nil {
		t.Fatalf("create %s from %s: %v", name, ref, err)
	}
	t.Cleanup(func() { removeDeployContainer(t, cli, name) })
}

// removeDeployContainer removes a test container with its anonymous volumes.
// The registry image declares one for /var/lib/registry, which stores the
// pushed images.
func removeDeployContainer(t *testing.T, cli *client.Client, name string) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inspected, err := cli.ContainerInspect(cleanup, name, client.ContainerInspectOptions{})
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
	if _, err := cli.ContainerRemove(cleanup, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Errorf("remove container %s: %v", name, err)
		return
	}
	for _, volume := range volumes {
		if _, err := cli.VolumeInspect(cleanup, volume, client.VolumeInspectOptions{}); err == nil {
			t.Errorf("volume %s of container %s remains after removal", volume, name)
		}
	}
	t.Logf("removed container %s with volumes %v", name, volumes)
}
