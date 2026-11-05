package testenv

import (
	"context"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) removeVolume(ctx context.Context, name string) error {
	if (!execution.anonymousVolumes[name] && !execution.fixtures.Volumes[name]) || execution.baseline.Volumes[name] || execution.cacheVolumes[name] || name == "" {
		return fmt.Errorf("refuse unproven temporary volume %s", name)
	}
	_, err := macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
		return execution.defaultClient.VolumeInspect(observation, name, client.VolumeInspectOptions{})
	})
	if errdefs.IsNotFound(err) {
		execution.recordVolumeAbsent(name)
		return nil
	}
	if err != nil {
		return macRunnerFailure("inspect temporary volume "+name, err)
	}
	listed, err := macObserve(ctx, func(observation context.Context) (client.ContainerListResult, error) {
		return execution.defaultClient.ContainerList(observation, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("volume", name)})
	})
	if err != nil {
		return macRunnerFailure("inspect consumers of temporary volume "+name, err)
	}
	if len(listed.Items) != 0 {
		return fmt.Errorf("preserve temporary volume %s with %d current consumers", name, len(listed.Items))
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.VolumeRemoveResult, error) {
		return execution.defaultClient.VolumeRemove(observation, name, client.VolumeRemoveOptions{})
	})
	execution.operation("volume remove", name, err)
	if err != nil {
		return macRunnerFailure("remove temporary volume "+name, err)
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
		return execution.defaultClient.VolumeInspect(observation, name, client.VolumeInspectOptions{})
	})
	if !errdefs.IsNotFound(err) {
		if err == nil {
			err = errors.New("volume still exists")
		}
		return macRunnerFailure("verify temporary volume "+name+" absence", err)
	}
	execution.recordVolumeAbsent(name)
	return nil
}

func (execution *macRunnerExecution) removeNetwork(ctx context.Context) error {
	if execution.networkID == "" || execution.baseline.Networks[execution.networkID] {
		return errors.New("refuse baseline or unidentified default network")
	}
	inspection, err := macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
		return execution.defaultClient.NetworkInspect(observation, execution.networkID, client.NetworkInspectOptions{})
	})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return macRunnerFailure("inspect created default network", err)
	}
	current := inspection.Network
	if current.Name != execution.prepared.project.Networks["default"].Name || current.Labels[macRunnerLabel] != execution.name || current.Labels[macRunnerProjectLabel] != execution.prepared.project.Name || len(current.Containers) != 0 {
		return fmt.Errorf("preserve default network %s without exact ownership or with current endpoints", execution.networkID)
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.NetworkRemoveResult, error) {
		return execution.defaultClient.NetworkRemove(observation, execution.networkID, client.NetworkRemoveOptions{})
	})
	execution.operation("network remove", execution.networkID, err)
	if err != nil {
		return macRunnerFailure("remove created default network "+execution.networkID, err)
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
		return execution.defaultClient.NetworkInspect(observation, execution.networkID, client.NetworkInspectOptions{})
	})
	if !errdefs.IsNotFound(err) {
		if err == nil {
			err = errors.New("network still exists")
		}
		return macRunnerFailure("verify network "+execution.networkID+" absence", err)
	}
	return nil
}
