package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// dataDiskImage pins the debian:bookworm-slim image by digest. The image
	// includes df, truncate, mkfs.ext4, and losetup.
	dataDiskImage = "debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251"
	dataDiskKind  = "opensearch-disk"
	// dataDiskPrefix starts the name of the helper container and of both
	// volumes.
	dataDiskPrefix = "tack-testenv-" + dataDiskKind + "-"
	// dataDiskMount is where the helper mounts the volume that stores the
	// image file.
	dataDiskMount = "/disk"
	// dataDiskFileName is the name of the image file in that volume.
	dataDiskFileName = "data.img"
	// dataDiskMarginBytes is the free space the Docker VM disk must keep
	// beyond the full size of the data disk.
	dataDiskMarginBytes int64 = 8 << 30
)

// openSearchDataDisk is a size-bounded ext4 filesystem in a sparse image
// file on the disk of the Docker VM. A privileged helper container attaches
// the file to a loop device for the life of the test, and a local volume
// mounts that device at the engine data path. The filesystem keeps no space
// back for root, and the opensearch user owns its top directory. df and
// OpenSearch then report the same free space.
type openSearchDataDisk struct {
	helper      string
	imageVolume string
	dataVolume  string
	device      string
}

// createOpenSearchDataDisk creates a filesystem of sizeBytes and a volume
// that mounts it. After an error it still returns the names of everything it
// created, and the caller removes them with removeOpenSearchDataDisk.
func createOpenSearchDataDisk(ctx context.Context, cli *client.Client, sizeBytes int64) (openSearchDataDisk, error) {
	var disk openSearchDataDisk
	base, err := generatedEngineName(ctx, dataDiskKind)
	if err != nil {
		return disk, err
	}
	labels := map[string]string{managedLabel: "true"}
	if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: base + "-image", Labels: labels}); err != nil {
		return disk, dataDiskFailure(ctx, "create the image volume "+base+"-image", err)
	}
	disk.imageVolume = base + "-image"
	mounts := []mount.Mount{{Type: mount.TypeVolume, Source: disk.imageVolume, Target: dataDiskMount}}
	disk.helper = base
	if err := startDataDiskHelper(ctx, cli, base, mounts); err != nil {
		return disk, err
	}
	if err := requireDataDiskSpace(ctx, cli, base, sizeBytes); err != nil {
		return disk, err
	}
	file := dataDiskMount + "/" + dataDiskFileName
	owner := strconv.Itoa(openSearchOwner.uid) + ":" + strconv.Itoa(openSearchOwner.gid)
	for _, command := range [][]string{
		{"truncate", "-s", strconv.FormatInt(sizeBytes, 10), file},
		{"mkfs.ext4", "-F", "-q", "-m", "0", "-E", "root_owner=" + owner, file},
	} {
		if _, err := runInDataDiskHelper(ctx, cli, base, command); err != nil {
			return disk, err
		}
	}
	device, err := runInDataDiskHelper(ctx, cli, base, []string{"losetup", "--find", "--show", file})
	if err != nil {
		// losetup may have attached a device before the output read failed.
		_, detachErr := detachDataDiskDevices(ctx, cli, base, file)
		return disk, dataDiskFailure(ctx, "attach "+file+" to a loop device", errors.Join(err, detachErr))
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

// dataDiskFailure logs and wraps one failed data disk step.
func dataDiskFailure(ctx context.Context, action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.data_disk_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
