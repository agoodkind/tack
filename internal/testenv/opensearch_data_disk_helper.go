package testenv

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// startDataDiskHelper starts a privileged helper container named name with
// mounts, and records it for Release. losetup creates the loop device node
// in the /dev of the Docker VM. A privileged container gets its own copy of
// /dev. The helper binds the VM /dev to see the new node.
func startDataDiskHelper(ctx context.Context, cli *client.Client, name string, mounts []mount.Mount) error {
	if err := ensureImage(ctx, cli, engineSpec{kind: dataDiskKind, image: dataDiskImage, platform: nil, cmd: nil, env: nil}); err != nil {
		return err
	}
	_, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: dataDiskImage, Entrypoint: []string{"sleep"}, Cmd: []string{openSearchHolderSeconds},
			Labels: map[string]string{managedLabel: "true"},
		},
		HostConfig: &container.HostConfig{
			Privileged: true, NetworkMode: "none",
			Mounts: append(mounts, mount.Mount{Type: mount.TypeBind, Source: "/dev", Target: "/dev"}),
		},
		Name: name,
	})
	if err != nil {
		return dataDiskFailure(ctx, "create the disk helper "+name, err)
	}
	own(name)
	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return dataDiskFailure(ctx, "start the disk helper "+name, err)
	}
	return nil
}

// requireDataDiskSpace reads the free space of the Docker VM disk through
// the image volume of helper. The fill writes nearly the whole data disk.
// requireDataDiskSpace returns an error when the free space is less than
// sizeBytes plus dataDiskMarginBytes.
func requireDataDiskSpace(ctx context.Context, cli *client.Client, helper string, sizeBytes int64) error {
	output, err := runInDataDiskHelper(ctx, cli, helper, []string{"df", "-B1", "--output=avail", dataDiskMount})
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	free, err := strconv.ParseInt(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
	if err != nil {
		return dataDiskFailure(ctx, "read the free space of the Docker VM disk from "+strconv.Quote(output), err)
	}
	needed := sizeBytes + dataDiskMarginBytes
	if free < needed {
		return dataDiskFailure(ctx, "check the Docker VM disk before the data disk", fmt.Errorf(
			"the Docker VM disk has %d bytes free; the %d byte data disk plus an %d byte margin needs %d bytes",
			free, sizeBytes, dataDiskMarginBytes, needed))
	}
	return nil
}

// detachDataDiskDevices detaches every loop device attached to file, a path
// inside helper, and returns the detached device names. losetup -j finds the
// devices by the device and inode of the file.
func detachDataDiskDevices(ctx context.Context, cli *client.Client, helper, file string) ([]string, error) {
	output, err := runInDataDiskHelper(ctx, cli, helper, []string{"losetup", "-j", file})
	if err != nil {
		return nil, err
	}
	var detached []string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		device, _, found := strings.Cut(line, ":")
		if !found || !strings.HasPrefix(device, "/dev/loop") {
			continue
		}
		if _, err := runInDataDiskHelper(ctx, cli, helper, []string{"losetup", "-d", device}); err != nil {
			return detached, err
		}
		detached = append(detached, device)
	}
	return detached, nil
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
