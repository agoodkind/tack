package testenv

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) snapshot(ctx context.Context) error {
	containers, err := macObserve(ctx, func(observation context.Context) (client.ContainerListResult, error) {
		return execution.defaultClient.ContainerList(observation, client.ContainerListOptions{All: true})
	})
	if err != nil {
		return macRunnerFailure("snapshot existing containers", err)
	}
	for _, item := range containers.Items {
		execution.baseline.Containers[item.ID] = true
		if item.Labels[macRunnerProjectLabel] == execution.prepared.project.Name && item.Labels[macRunnerServiceLabel] == "tests" {
			return fmt.Errorf("refuse existing tests-service container %s for project %s", item.ID, execution.prepared.project.Name)
		}
		if slices.Contains(item.Names, "/"+execution.name) {
			return fmt.Errorf("refuse existing generated container name %s", execution.name)
		}
	}
	networks, err := macObserve(ctx, func(observation context.Context) (client.NetworkListResult, error) {
		return execution.defaultClient.NetworkList(observation, client.NetworkListOptions{})
	})
	if err != nil {
		return macRunnerFailure("snapshot existing networks", err)
	}
	for _, item := range networks.Items {
		execution.baseline.Networks[item.ID] = true
	}
	volumes, err := macObserve(ctx, func(observation context.Context) (client.VolumeListResult, error) {
		return execution.defaultClient.VolumeList(observation, client.VolumeListOptions{})
	})
	if err != nil {
		return macRunnerFailure("snapshot existing volumes", err)
	}
	for _, item := range volumes.Items {
		execution.baseline.Volumes[item.Name] = true
	}
	execution.baselineReady = true
	execution.write("baseline-containers.stdout", macRunnerIdentities(execution.baseline.Containers))
	execution.write("baseline-networks.stdout", macRunnerIdentities(execution.baseline.Networks))
	execution.write("baseline-volumes.stdout", macRunnerIdentities(execution.baseline.Volumes))
	for _, item := range execution.prepared.service.Volumes {
		if item.Target != "/root/.cache/go-build" && item.Target != "/go/pkg/mod" {
			continue
		}
		execution.cacheVolumes[item.Source] = true
		if !execution.baseline.Volumes[item.Source] {
			return fmt.Errorf("require existing project cache volume %s", item.Source)
		}
	}
	return macRunnerErrors("record resource baseline", execution.evidenceErrors...)
}

func macRunnerIdentities(identities map[string]bool) string {
	values := make([]string, 0, len(identities))
	for identity := range identities {
		values = append(values, identity)
	}
	slices.Sort(values)
	return strings.Join(values, "\n") + "\n"
}
