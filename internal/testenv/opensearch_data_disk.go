package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// dataDiskImage includes truncate, mkfs.ext4, and losetup. The digest is
	// the debian:bookworm-slim index digest read on 2026-10-02.
	dataDiskImage = "debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251"
	// dataDiskMount is where the helper mounts the volume that stores the
	// filesystem image file.
	dataDiskMount = "/disk"
	// dataDiskFile is the sparse filesystem image file.
	dataDiskFile = dataDiskMount + "/data.img"
)

// openSearchDataDisk is a size-bounded ext4 filesystem in a sparse image
// file on the Docker host disk. A privileged helper container attaches the
// file to a loop device for the life of the test, and a local volume mounts
// that device at the engine data path. The filesystem reserves no blocks
// and its root belongs to the opensearch user, so df and the disk threshold
// monitor both read the whole filesystem.
type openSearchDataDisk struct {
	helper      string
	imageVolume string
	dataVolume  string
	device      string
}

// createOpenSearchDataDisk creates a filesystem of sizeBytes and the volume
// that mounts it. It returns every name it created, also on failure, and
// removeOpenSearchDataDisk removes them.
func createOpenSearchDataDisk(ctx context.Context, cli *client.Client, sizeBytes int64) (openSearchDataDisk, error) {
	var disk openSearchDataDisk
	base, err := generatedEngineName(ctx, "opensearch-disk")
	if err != nil {
		return disk, err
	}
	labels := map[string]string{managedLabel: "true"}
	if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: base + "-image", Labels: labels}); err != nil {
		return disk, dataDiskFailure(ctx, "create the image volume "+base+"-image", err)
	}
	disk.imageVolume = base + "-image"
	if err := ensureImage(ctx, cli, engineSpec{kind: "opensearch-disk", image: dataDiskImage, platform: nil, cmd: nil, env: nil}); err != nil {
		return disk, err
	}
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: dataDiskImage, Entrypoint: []string{"sleep"}, Cmd: []string{openSearchHolderSeconds}, Labels: labels,
		},
		HostConfig: &container.HostConfig{
			// The loop device that losetup allocates appears in the host
			// /dev, which a privileged container sees only through a bind.
			Privileged: true, NetworkMode: "none",
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: disk.imageVolume, Target: dataDiskMount},
				{Type: mount.TypeBind, Source: "/dev", Target: "/dev"},
			},
		},
		Name: base,
	})
	if err != nil {
		return disk, dataDiskFailure(ctx, "create the disk helper "+base, err)
	}
	own(base)
	disk.helper = base
	if _, err := cli.ContainerStart(ctx, base, client.ContainerStartOptions{}); err != nil {
		return disk, dataDiskFailure(ctx, "start the disk helper "+base, err)
	}
	owner := strconv.Itoa(openSearchOwner.uid) + ":" + strconv.Itoa(openSearchOwner.gid)
	for _, command := range [][]string{
		{"truncate", "-s", strconv.FormatInt(sizeBytes, 10), dataDiskFile},
		{"mkfs.ext4", "-F", "-q", "-m", "0", "-E", "root_owner=" + owner, dataDiskFile},
	} {
		if _, err := runInDataDiskHelper(ctx, cli, base, command); err != nil {
			return disk, err
		}
	}
	device, err := runInDataDiskHelper(ctx, cli, base, []string{"losetup", "--find", "--show", dataDiskFile})
	if err != nil {
		return disk, err
	}
	disk.device = strings.TrimSpace(device)
	_, err = cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: base + "-data", Driver: "local", Labels: labels,
		DriverOpts: map[string]string{"type": "ext4", "device": disk.device},
	})
	if err != nil {
		return disk, dataDiskFailure(ctx, "create the data volume "+base+"-data on "+disk.device, err)
	}
	disk.dataVolume = base + "-data"
	telemetry.L(ctx).InfoContext(ctx, "search.data_disk_created", slog.String("device", disk.device),
		slog.String("volume", disk.dataVolume), slog.Int64("size_bytes", sizeBytes))
	return disk, nil
}

// removeOpenSearchDataDisk removes the data volume, detaches the loop
// device, removes the helper, and removes the image volume, in that order.
// It runs after the engine is removed. A name that is empty or already gone
// counts as removed.
func removeOpenSearchDataDisk(ctx context.Context, cli *client.Client, disk openSearchDataDisk) error {
	var failures []error
	failures = append(failures, removeDataDiskVolume(ctx, cli, disk.dataVolume))
	if disk.device != "" {
		_, err := runInDataDiskHelper(ctx, cli, disk.helper, []string{"losetup", "-d", disk.device})
		failures = append(failures, err)
	}
	if disk.helper != "" {
		failures = append(failures, removeContainersWith(ctx, cli, []string{disk.helper}))
	}
	failures = append(failures, removeDataDiskVolume(ctx, cli, disk.imageVolume))
	if err := errors.Join(failures...); err != nil {
		return dataDiskFailure(ctx, "remove the data disk "+disk.helper, err)
	}
	return nil
}

// removeDataDiskVolume removes one volume of the data disk.
func removeDataDiskVolume(ctx context.Context, cli *client.Client, name string) error {
	if name == "" {
		return nil
	}
	if _, err := cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: false}); err != nil && !cerrdefs.IsNotFound(err) {
		return dataDiskFailure(ctx, "remove volume "+name, err)
	}
	return nil
}

// runInDataDiskHelper runs command in the helper and returns its output. A
// nonzero exit is an error.
func runInDataDiskHelper(ctx context.Context, cli *client.Client, helper string, command []string) (string, error) {
	output, code, err := execInContainer(ctx, cli, helper, command)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", dataDiskFailure(ctx, fmt.Sprintf("run %v in %s", command, helper),
			fmt.Errorf("exit %d: %s", code, strings.TrimSpace(output)))
	}
	return output, nil
}

// dataDiskFailure logs and wraps one failed data disk step.
func dataDiskFailure(ctx context.Context, action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.data_disk_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
