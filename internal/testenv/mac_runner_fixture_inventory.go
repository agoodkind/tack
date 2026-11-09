package testenv

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) attributeFixtures(ctx context.Context) error {
	if execution.acceptedID == "" {
		return nil
	}

	logs, logErr := execution.fixtureLogs()
	failures := []error{logErr, execution.observerError}
	creates := execution.eventSnapshot.copy()
	containerCreates := make(map[string]bool)

	for _, observed := range creates {
		if observed.Event.Type != events.ContainerEventType {
			continue
		}

		id := observed.Event.Actor.ID
		containerCreates[id] = true
		if macRunnerContainerID.MatchString(observed.Event.Actor.Attributes[runnerLabel]) &&
			observed.Event.Actor.Attributes[runnerLabel] != execution.acceptedID {
			execution.fixtures.OtherRunners["container:"+id] = observed.Event.Actor.Attributes[runnerLabel]
			execution.preserveFixture(
				"container",
				id,
				"create event proves another runner owns this resource",
				false,
			)
			continue
		}

		if observed.Container != nil {
			execution.attributeContainer(
				*observed.Container,
				true,
				logs[observed.Event.Actor.Attributes["name"]],
			)
		} else if !execution.baseline.Containers[id] && execution.candidateFixture(
			"container",
			id,
			observed.Event.Actor.Attributes,
		) {
			reason := "create inspection failed: " + observed.Error
			execution.preserveFixture("container", id, reason, true)
			failures = append(failures, fmt.Errorf("fixture %s %s", id, reason))
		}
	}

	failures = append(failures, execution.inventoryFixtureContainers(ctx, containerCreates, logs))
	execution.attributeDiskVolumes(creates)

	failures = append(failures, execution.inventoryFixtureDiskVolumes(ctx))
	for id, fixture := range execution.fixtures.Containers {
		if execution.diskHelper(fixture) {
			failures = append(failures, execution.inventoryDiskHelper(ctx, id))
		}
	}

	for _, observed := range creates {
		if observed.Network != nil {
			execution.attributeNetwork(*observed.Network, true)
		}
	}

	failures = append(failures, execution.inventoryFixtureNetworks(ctx))
	for _, item := range execution.fixtures.Preserved {
		if item.Incomplete {
			failures = append(
				failures,
				fmt.Errorf("preserve %s %s: %s", item.Kind, item.ID, item.Reason),
			)
		}
	}

	return macRunnerErrors("attribute inner fixtures", failures...)
}

func (execution *macRunnerExecution) inventoryFixtureDiskVolumes(ctx context.Context) error {
	filters := client.Filters{}.Add(
		"label",
		managedLabel+"=true",
	).Add(
		"label",
		runnerLabel+"="+execution.acceptedID,
	)
	volumes, err := macObserve(
		ctx,
		func(observation context.Context) (client.VolumeListResult, error) {
			return execution.defaultClient.VolumeList(
				observation,
				client.VolumeListOptions{Filters: filters},
			)
		},
	)
	if err != nil {
		return macRunnerFailure("inventory runner disk volumes", err)
	}

	if len(volumes.Warnings) != 0 || len(volumes.Items) > macRunnerInspectionLimit {
		return macRunnerFailure(
			"validate runner volume inventory",
			fmt.Errorf(
				"inventory is incomplete or exceeds %d entries: %v",
				macRunnerInspectionLimit,
				volumes.Warnings,
			),
		)
	}

	var failures []error

	for _, item := range volumes.Items {
		if !strings.HasPrefix(item.Name, dataDiskPrefix) ||
			(!strings.HasSuffix(item.Name, "-image") && !strings.HasSuffix(item.Name, "-data")) ||
			execution.baseline.Volumes[item.Name] ||
			execution.cacheVolumes[item.Name] {
			continue
		}

		inspection, inspectErr := macObserve(
			ctx,
			func(observation context.Context) (client.VolumeInspectResult, error) {
				return execution.defaultClient.VolumeInspect(
					observation,
					item.Name,
					client.VolumeInspectOptions{},
				)
			},
		)
		if errdefs.IsNotFound(inspectErr) {
			continue
		}

		if inspectErr != nil {
			execution.fixtures.Volumes[item.Name] = false
			execution.preserveFixture(
				"volume",
				item.Name,
				"disk volume inspection failed: "+inspectErr.Error(),
				true,
			)
			failures = append(
				failures,
				macRunnerFailure("inspect runner disk volume "+item.Name, inspectErr),
			)
			continue
		}

		volume := inspection.Volume
		if volume.Name != item.Name || !execution.fixtureOwner(volume.Labels) {
			execution.fixtures.Volumes[item.Name] = false
			execution.preserveFixture("volume", item.Name, "disk volume ownership changed", true)
			continue
		}

		if _, recorded := execution.fixtures.NamedVolumes[item.Name]; recorded {
			if verifyErr := execution.verifyDiskVolume(ctx, item.Name); verifyErr != nil {
				execution.fixtures.Volumes[item.Name] = false
				execution.preserveFixture("volume", item.Name, verifyErr.Error(), true)
				failures = append(failures, verifyErr)
				continue
			}
		}

		if _, recorded := execution.fixtures.Volumes[item.Name]; !recorded {
			execution.fixtures.Volumes[item.Name] = false
			execution.preserveFixture(
				"volume",
				item.Name,
				"disk volume lacks matching helper ownership",
				true,
			)
		}

		var observed macRunnerObservedCreate
		observed.Volume = &volume
		execution.attributeDiskVolumes([]macRunnerObservedCreate{observed})
	}

	return macRunnerErrors("inventory runner disk volumes", failures...)
}

func (execution *macRunnerExecution) inventoryFixtureContainers(
	ctx context.Context,
	containerCreates map[string]bool,
	logs map[string]bool,
) error {
	var failures []error
	containers, err := macObserve(
		ctx,
		func(observation context.Context) (client.ContainerListResult, error) {
			return execution.defaultClient.ContainerList(
				observation,
				client.ContainerListOptions{All: true},
			)
		},
	)
	if err != nil {
		return macRunnerFailure("inventory inner containers", err)
	}

	for _, item := range containers.Items {
		if execution.baseline.Containers[item.ID] || item.Labels[managedLabel] != "true" {
			continue
		}

		inspection, inspectErr := execution.inspect(ctx, item.ID)
		if errdefs.IsNotFound(inspectErr) {
			continue
		}

		if inspectErr != nil {
			failures = append(failures, inspectErr)
			continue
		}

		execution.attributeContainer(
			inspection,
			containerCreates[item.ID],
			logs[strings.TrimPrefix(inspection.Name, "/")],
		)
	}

	return macRunnerErrors("inventory inner containers", failures...)
}

func (execution *macRunnerExecution) inventoryFixtureNetworks(ctx context.Context) error {
	var failures []error
	networks, err := macObserve(
		ctx,
		func(observation context.Context) (client.NetworkListResult, error) {
			return execution.defaultClient.NetworkList(observation, client.NetworkListOptions{})
		},
	)
	if err != nil {
		return macRunnerFailure("inventory inner networks", err)
	}

	for _, item := range networks.Items {
		if execution.baseline.Networks[item.ID] || item.Labels[managedLabel] != "true" {
			continue
		}

		inspection, inspectErr := macObserve(
			ctx,
			func(observation context.Context) (client.NetworkInspectResult, error) {
				return execution.defaultClient.NetworkInspect(
					observation,
					item.ID,
					client.NetworkInspectOptions{},
				)
			},
		)
		if errdefs.IsNotFound(inspectErr) {
			continue
		}

		if inspectErr != nil {
			failures = append(failures, inspectErr)
			continue
		}

		execution.attributeNetwork(inspection.Network, false)
	}

	return macRunnerErrors("inventory inner networks", failures...)
}

func (execution *macRunnerExecution) attributeNetwork(inspection network.Inspect, created bool) {
	if execution.baseline.Networks[inspection.ID] || inspection.ID == execution.networkID {
		return
	}

	if inspection.Name == networkName && !execution.fixtureOwner(inspection.Labels) {
		return
	}

	if !execution.candidateFixture("network", inspection.ID, inspection.Labels) {
		return
	}

	if !execution.fixtureOwner(inspection.Labels) {
		execution.preserveFixture(
			"network",
			inspection.ID,
			"managed network lacks exact invocation attribution",
			true,
		)
		return
	}

	previous := execution.fixtures.Networks[inspection.ID]
	execution.fixtures.Networks[inspection.ID] = macRunnerFixture{
		ID:     inspection.ID,
		Name:   inspection.Name,
		Labels: inspection.Labels,
		Proof:  "owner labels",
		CreateObserved: created ||
			previous.CreateObserved,
		Absent:      previous.Absent,
		LogObserved: false,
		Volumes:     nil,
	}
}
