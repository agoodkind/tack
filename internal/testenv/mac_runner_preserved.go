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
		failures = append(failures, execution.comparePreserved("container", execution.baseline.Containers, current, execution.preserved.Containers, execution.projectContainers))
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
		failures = append(failures, execution.comparePreserved("network", execution.baseline.Networks, current, execution.preserved.Networks, map[string]bool{execution.networkID: true}))
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
		failures = append(failures, execution.comparePreserved("volume", execution.baseline.Volumes, current, execution.preserved.Volumes, execution.cacheVolumes))
	}
	return macRunnerErrors("verify preserved baseline", failures...)
}

// comparePreserved records surviving baseline identities in preserved.
// It appends missing identities outside project to removedElsewhere and
// returns an error for missing identities in project.
func (execution *macRunnerExecution) comparePreserved(kind string, baseline, current, preserved, project map[string]bool) error {
	removed, err := macRunnerPreserved(kind, baseline, current, preserved, project)
	execution.removedElsewhere = append(execution.removedElsewhere, removed...)
	return err
}

// macRunnerPreserved records surviving baseline identities in preserved.
// It returns missing identities outside project and an error for missing
// identities in project.
func macRunnerPreserved(kind string, baseline, current, preserved, project map[string]bool) ([]string, error) {
	var failures []error
	var removed []string
	for identity := range baseline {
		switch {
		case current[identity]:
			preserved[identity] = true
		case project[identity]:
			failures = append(failures, fmt.Errorf("baseline %s %s disappeared during invocation", kind, identity))
		default:
			removed = append(removed, kind+" "+identity)
		}
	}
	return removed, macRunnerErrors("compare baseline identities", failures...)
}
