package testenv

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) attributeDiskVolumes(creates []macRunnerObservedCreate) {
	for _, observed := range creates {
		if observed.Volume == nil {
			continue
		}
		volume := *observed.Volume
		if execution.baseline.Volumes[volume.Name] || execution.cacheVolumes[volume.Name] || !execution.fixtureOwner(volume.Labels) {
			continue
		}
		if !strings.HasPrefix(volume.Name, dataDiskPrefix+volume.Labels[processLabel]+"-") || (!strings.HasSuffix(volume.Name, "-image") && !strings.HasSuffix(volume.Name, "-data")) {
			continue
		}
		if _, recorded := execution.fixtures.Volumes[volume.Name]; !recorded {
			execution.fixtures.Volumes[volume.Name] = false
			execution.preserveFixture("volume", volume.Name, "disk creation lacks matching helper ownership", true)
		}
		for _, fixture := range execution.fixtures.Containers {
			if !macRunnerSameFixtureOwner(volume.Labels, fixture.Labels) {
				continue
			}
			if macRunnerDiskVolume(fixture, volume) {
				execution.fixtures.Volumes[volume.Name] = true
				execution.fixtures.NamedVolumes[volume.Name] = volume
				delete(execution.fixtures.Preserved, "volume:"+volume.Name)
			}
		}
	}
}

func macRunnerDiskVolume(helper macRunnerFixture, diskVolume volume.Volume) bool {
	if !strings.HasPrefix(helper.Name, dataDiskPrefix) || diskVolume.Driver != "local" {
		return false
	}
	for _, point := range helper.Volumes {
		if point.Name != helper.Name+"-image" || point.Target != dataDiskMount {
			continue
		}
		if diskVolume.Name == point.Name {
			return true
		}
		return diskVolume.Name == helper.Name+"-data" && diskVolume.Options["type"] == "ext4" && strings.HasPrefix(diskVolume.Options["device"], "/dev/loop")
	}
	return false
}

func (execution *macRunnerExecution) diskHelper(fixture macRunnerFixture) bool {
	_, proven := execution.fixtures.NamedVolumes[fixture.Name+"-image"]
	return proven && execution.fixtures.Volumes[fixture.Name+"-image"]
}

func (execution *macRunnerExecution) cleanupDiskHelper(ctx context.Context, id string) error {
	helper := execution.fixtures.Containers[id]
	imageName, dataName := helper.Name+"-image", helper.Name+"-data"
	if proven, candidate := execution.fixtures.Volumes[dataName]; candidate && !proven {
		return fmt.Errorf("preserve disk helper %s with unproven data volume %s", id, dataName)
	}
	for _, name := range []string{imageName, dataName} {
		if _, proven := execution.fixtures.NamedVolumes[name]; !proven {
			continue
		}
		if err := execution.fixtureVolumeConsumers(ctx, name, name == dataName); err != nil {
			return err
		}
	}
	if execution.fixtures.Volumes[dataName] {
		if err := execution.removeVolume(ctx, dataName); err != nil {
			return err
		}
	}
	inspection, err := execution.inspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inspection.Config == nil || !maps.Equal(inspection.Config.Labels, helper.Labels) {
		return fmt.Errorf("preserve disk helper %s after ownership changed", id)
	}
	if _, err := detachDataDiskDevices(ctx, execution.defaultClient, helper.Name, dataDiskMount+"/"+dataDiskFileName); err != nil {
		return err
	}
	return execution.removeFixture(ctx, id)
}

func macRunnerSameFixtureOwner(left, right map[string]string) bool {
	for _, key := range []string{managedLabel, runnerLabel, hostLabel, processLabel, runLabel} {
		if left[key] == "" || left[key] != right[key] {
			return false
		}
	}
	return true
}

func (execution *macRunnerExecution) verifyDiskVolume(ctx context.Context, name string) error {
	recorded, named := execution.fixtures.NamedVolumes[name]
	if !named {
		return nil
	}
	inspection, err := macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
		return execution.defaultClient.VolumeInspect(observation, name, client.VolumeInspectOptions{})
	})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return macRunnerFailure("inspect disk volume "+name, err)
	}
	if inspection.Volume.Name != recorded.Name || inspection.Volume.Driver != recorded.Driver || !maps.Equal(inspection.Volume.Labels, recorded.Labels) || !maps.Equal(inspection.Volume.Options, recorded.Options) || inspection.Volume.CreatedAt != recorded.CreatedAt {
		return fmt.Errorf("preserve disk volume %s after ownership changed", name)
	}
	return nil
}
