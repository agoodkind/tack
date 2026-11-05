//go:build macrunnerfixture && runtimeaudit

package testenv

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func macRunnerDiskFixture(t *testing.T, ctx context.Context, docker *client.Client, image string) {
	t.Helper()
	const sizeBytes = 32 << 20
	disk, err := createOpenSearchDataDisk(ctx, docker, sizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := prepareCreate(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}, Labels: ownerLabels(owner)},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: disk.dataVolume, Target: openSearchDataPath}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
}
