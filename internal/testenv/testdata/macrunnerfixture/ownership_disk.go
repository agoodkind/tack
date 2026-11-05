//go:build macrunnerfixture && runtimeaudit

package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func macRunnerDiskFixture(t *testing.T, ctx context.Context, docker *client.Client, image, mode string) {
	t.Helper()
	if mode == "disk-image-killed" {
		name, err := generatedEngineName(ctx, dataDiskKind)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := prepareCreate(ctx, docker)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name + "-image", Labels: ownerLabels(owner)}); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	}
	const sizeBytes = 32 << 20
	disk, err := createOpenSearchDataDisk(ctx, docker, sizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "disk-killed" {
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
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

func macRunnerPartialOwnerFixture(t *testing.T, ctx context.Context, docker *client.Client, image string, owner processOwner) {
	t.Helper()
	name, err := generatedEngineName(ctx, "foundationdb")
	if err != nil {
		t.Fatal(err)
	}
	labels := ownerLabels(owner)
	delete(labels, runnerLabel)
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name, Config: &container.Config{Image: image, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}, Labels: labels},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/fixture-volume"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	slog.Info("testenv.engine.started", slog.String("container", name), slog.String("image", image))
	fmt.Printf("MAC_PARTIAL_OWNER_ID=%s PID=%d\n", created.ID, os.Getpid())
	time.Sleep(4 * time.Minute)
}
