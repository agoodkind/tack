package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/telemetry"
)

// removeLeftoverDataDisks removes the data disks that an earlier run of a
// disposable engine left behind when the test binary was killed before its
// cleanup ran. Each volume it removes starts with dataDiskPrefix. It removes
// every container that uses one of those volumes, then the data volumes,
// then the loop devices attached to the image files, then the image
// volumes. It returns one line for each removed item.
//
// It removes every matching volume without checking which process created
// it. This is safe because only one engine test runs at a time on the shared
// test slot. A test with a data disk that runs at the same time in another
// process would lose that disk.
func removeLeftoverDataDisks(ctx context.Context, cli *client.Client) ([]string, error) {
	listed, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("name", dataDiskPrefix)})
	if err != nil {
		return nil, dataDiskFailure(ctx, "list leftover data disk volumes", err)
	}
	var dataVolumes, imageVolumes []string
	for _, item := range listed.Items {
		switch {
		case !strings.HasPrefix(item.Name, dataDiskPrefix):
		case strings.HasSuffix(item.Name, "-data"):
			dataVolumes = append(dataVolumes, item.Name)
		case strings.HasSuffix(item.Name, "-image"):
			imageVolumes = append(imageVolumes, item.Name)
		}
	}
	if len(dataVolumes)+len(imageVolumes) == 0 {
		return nil, nil
	}
	var removed []string
	containers, err := leftoverDataDiskContainers(ctx, cli, slices.Concat(dataVolumes, imageVolumes))
	if err != nil {
		return removed, err
	}
	if err := removeContainersWith(ctx, cli, containers); err != nil {
		return removed, err
	}
	for _, name := range containers {
		removed = append(removed, "container "+name)
	}
	for _, name := range dataVolumes {
		if err := removeDataDiskVolume(ctx, cli, name); err != nil {
			return removed, err
		}
		removed = append(removed, "volume "+name)
	}
	devices, err := detachLeftoverDataDiskDevices(ctx, cli, imageVolumes)
	for _, device := range devices {
		removed = append(removed, "loop device "+device)
	}
	if err != nil {
		return removed, err
	}
	for _, name := range imageVolumes {
		if err := removeDataDiskVolume(ctx, cli, name); err != nil {
			return removed, err
		}
		removed = append(removed, "volume "+name)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.data_disk_leftovers_removed", slog.String("removed", strings.Join(removed, ", ")))
	return removed, nil
}

// leftoverDataDiskContainers returns every container, running or stopped,
// that uses one of volumes.
func leftoverDataDiskContainers(ctx context.Context, cli *client.Client, volumes []string) ([]string, error) {
	var containers []string
	for _, volume := range volumes {
		listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("volume", volume)})
		if err != nil {
			return nil, dataDiskFailure(ctx, "list the containers that use volume "+volume, err)
		}
		for _, item := range listed.Items {
			container := strings.TrimPrefix(strings.Join(item.Names, ","), "/")
			if !slices.Contains(containers, container) {
				containers = append(containers, container)
			}
		}
	}
	return containers, nil
}

// detachLeftoverDataDiskDevices starts a short-lived helper with each image
// volume mounted, detaches every loop device attached to an image file in
// them, and removes the helper. It returns the detached devices.
func detachLeftoverDataDiskDevices(ctx context.Context, cli *client.Client, imageVolumes []string) ([]string, error) {
	if len(imageVolumes) == 0 {
		return nil, nil
	}
	helper, err := generatedEngineName(ctx, dataDiskKind+"-cleanup")
	if err != nil {
		return nil, err
	}
	mounts := make([]mount.Mount, 0, len(imageVolumes))
	files := make([]string, 0, len(imageVolumes))
	for position, volume := range imageVolumes {
		target := "/leftover/" + strconv.Itoa(position)
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: volume, Target: target})
		files = append(files, target+"/"+dataDiskFileName)
	}
	startErr := startDataDiskHelper(ctx, cli, helper, mounts)
	var detached []string
	var failures []error
	if startErr == nil {
		for _, file := range files {
			_, code, err := execInContainer(ctx, cli, helper, []string{"test", "-f", file})
			if err != nil {
				failures = append(failures, fmt.Errorf("check for image file %s in %s: %w", file, helper, err))
				continue
			}
			if code != 0 {
				// A run killed before truncate left no image file.
				continue
			}
			devices, err := detachDataDiskDevices(ctx, cli, helper, file)
			detached = append(detached, devices...)
			failures = append(failures, err)
		}
	}
	failures = append(failures, startErr, removeContainersWith(ctx, cli, []string{helper}))
	if err := errors.Join(failures...); err != nil {
		return detached, dataDiskFailure(ctx, "detach leftover loop devices with helper "+helper, err)
	}
	return detached, nil
}
