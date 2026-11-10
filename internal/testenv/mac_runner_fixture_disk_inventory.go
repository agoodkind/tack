package testenv

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) inventoryDiskHelper(ctx context.Context, id string) error {
	helper := execution.fixtures.Containers[id]
	if helper.Absent {
		return nil
	}
	inspection, err := execution.inspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inspection.Config == nil || !execution.fixtureOwner(helper.Labels) || !maps.Equal(inspection.Config.Labels, helper.Labels) || strings.TrimPrefix(inspection.Name, "/") != helper.Name {
		return fmt.Errorf("preserve disk helper %s without exact ownership", id)
	}
	imageName := helper.Name + "-image"
	mounted := false
	for _, point := range inspection.Mounts {
		if point.Type == mount.TypeVolume && point.Name == imageName && point.Destination == dataDiskMount {
			mounted = true
		}
	}
	if !mounted {
		return fmt.Errorf("preserve disk helper %s without its image mount", id)
	}
	var failures []error
	for _, name := range []string{imageName, helper.Name + "-data"} {
		if execution.baseline.Volumes[name] || execution.cacheVolumes[name] {
			reason := "disk volume belongs to the baseline or cache"
			execution.fixtures.Volumes[name] = false
			execution.preserveFixture("volume", name, reason, true)
			failures = append(failures, fmt.Errorf("preserve disk volume %s: %s", name, reason))
			continue
		}
		result, inspectErr := macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
			return execution.defaultClient.VolumeInspect(observation, name, client.VolumeInspectOptions{})
		})
		if errdefs.IsNotFound(inspectErr) && name != imageName {
			delete(execution.fixtures.Volumes, name)
			delete(execution.fixtures.Preserved, "volume:"+name)
			continue
		}
		if inspectErr != nil {
			execution.fixtures.Volumes[name] = false
			execution.preserveFixture("volume", name, "disk volume inspection failed: "+inspectErr.Error(), true)
			failures = append(failures, macRunnerFailure("inspect disk volume "+name, inspectErr))
			continue
		}
		volume := result.Volume
		if volume.Name != name || !macRunnerSameFixtureOwner(volume.Labels, helper.Labels) || !macRunnerDiskVolume(helper, volume) {
			execution.fixtures.Volumes[name] = false
			execution.preserveFixture("volume", name, "disk volume lacks exact ownership", true)
			failures = append(failures, fmt.Errorf("preserve disk volume %s without exact ownership", name))
			continue
		}
		if _, recorded := execution.fixtures.NamedVolumes[name]; recorded {
			if err := execution.verifyDiskVolume(ctx, name); err != nil {
				execution.fixtures.Volumes[name] = false
				execution.preserveFixture("volume", name, err.Error(), true)
				failures = append(failures, err)
				continue
			}
		}
		execution.fixtures.Volumes[name] = true
		execution.fixtures.NamedVolumes[name] = volume
		delete(execution.fixtures.Preserved, "volume:"+name)
	}
	return macRunnerErrors("inventory disk helper "+id, failures...)
}
