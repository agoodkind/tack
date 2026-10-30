package testenv

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) verifyPreserved(ctx context.Context) error {
	var failures []error
	containers, containerErr := macObserve(ctx, func(observation context.Context) (client.ContainerListResult, error) {
		return execution.defaultClient.ContainerList(observation, client.ContainerListOptions{All: true})
	})
	if containerErr != nil {
		failures = append(failures, macRunnerFailure("verify baseline containers", containerErr))
	} else {
		current := make(map[string]bool)
		for _, item := range containers.Items {
			current[item.ID] = true
		}
		failures = append(failures, macRunnerPreserved("container", execution.baseline.Containers, current, execution.preserved.Containers))
	}
	networks, networkErr := macObserve(ctx, func(observation context.Context) (client.NetworkListResult, error) {
		return execution.defaultClient.NetworkList(observation, client.NetworkListOptions{})
	})
	if networkErr != nil {
		failures = append(failures, macRunnerFailure("verify baseline networks", networkErr))
	} else {
		current := make(map[string]bool)
		for _, item := range networks.Items {
			current[item.ID] = true
		}
		failures = append(failures, macRunnerPreserved("network", execution.baseline.Networks, current, execution.preserved.Networks))
	}
	volumes, volumeErr := macObserve(ctx, func(observation context.Context) (client.VolumeListResult, error) {
		return execution.defaultClient.VolumeList(observation, client.VolumeListOptions{})
	})
	if volumeErr != nil {
		failures = append(failures, macRunnerFailure("verify baseline volumes", volumeErr))
	} else {
		current := make(map[string]bool)
		for _, item := range volumes.Items {
			current[item.Name] = true
		}
		failures = append(failures, macRunnerPreserved("volume", execution.baseline.Volumes, current, execution.preserved.Volumes))
	}
	return macRunnerErrors("verify preserved baseline", failures...)
}

func macRunnerPreserved(kind string, baseline, current, preserved map[string]bool) error {
	var failures []error
	for identity := range baseline {
		if !current[identity] {
			failures = append(failures, fmt.Errorf("baseline %s %s disappeared during invocation", kind, identity))
			continue
		}
		preserved[identity] = true
	}
	return macRunnerErrors("compare baseline identities", failures...)
}
