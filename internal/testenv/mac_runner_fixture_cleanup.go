package testenv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) cleanupFixtures(ctx context.Context) error {
	if execution.acceptedID == "" {
		return nil
	}
	if execution.terminal == nil || execution.terminal.State == nil || execution.terminal.State.Running {
		return errors.New("preserve inner fixtures until the outer runner has stopped")
	}
	var failures []error
	for name, anonymous := range execution.fixtures.Volumes {
		if anonymous {
			failures = append(failures, execution.fixtureVolumeConsumers(ctx, name, false))
		}
	}
	for id := range execution.fixtures.Containers {
		failures = append(failures, execution.removeFixture(ctx, id))
	}
	for id := range execution.fixtures.Networks {
		failures = append(failures, execution.removeFixtureNetwork(ctx, id))
	}
	for name, anonymous := range execution.fixtures.Volumes {
		if !anonymous || execution.fixtures.Preserved["volume:"+name].Incomplete {
			continue
		}
		if err := execution.fixtureVolumeConsumers(ctx, name, true); err != nil {
			failures = append(failures, err)
			continue
		}
		failures = append(failures, execution.removeVolume(ctx, name))
	}
	return macRunnerErrors("clean inner fixtures", failures...)
}

func (execution *macRunnerExecution) removeFixture(ctx context.Context, id string) error {
	fixture, recorded := execution.fixtures.Containers[id]
	if !recorded || execution.baseline.Containers[id] {
		return fmt.Errorf("refuse unrecorded or baseline inner fixture %s", id)
	}
	inspection, err := execution.inspect(ctx, id)
	if errdefs.IsNotFound(err) {
		fixture.Absent = true
		execution.fixtures.Containers[id] = fixture
		return nil
	}
	if err != nil {
		return err
	}
	if inspection.Config == nil || inspection.ID != id || strings.TrimPrefix(inspection.Name, "/") != fixture.Name || !maps.Equal(inspection.Config.Labels, fixture.Labels) {
		return fmt.Errorf("preserve inner fixture %s after ownership changed", id)
	}
	// Inventory every volume before removing the container.
	execution.attributeContainer(inspection, fixture.CreateObserved, fixture.LogObserved)
	_, err = macObserve(ctx, func(observation context.Context) (client.ContainerRemoveResult, error) {
		return execution.defaultClient.ContainerRemove(observation, id, client.ContainerRemoveOptions{Force: true})
	})
	execution.operation("fixture container remove", id, err)
	if err != nil {
		return macRunnerFailure("remove inner fixture "+id, err)
	}
	_, err = execution.inspect(ctx, id)
	if !errdefs.IsNotFound(err) {
		return macRunnerAbsence("inner fixture", id, err)
	}
	fixture = execution.fixtures.Containers[id]
	fixture.Absent = true
	execution.fixtures.Containers[id] = fixture
	return nil
}

func (execution *macRunnerExecution) fixtureVolumeConsumers(ctx context.Context, name string, requireEmpty bool) error {
	if execution.baseline.Volumes[name] || execution.cacheVolumes[name] || !execution.fixtures.Volumes[name] {
		return fmt.Errorf("refuse baseline, cache or unproven inner volume %s", name)
	}
	consumers, err := macObserve(ctx, func(observation context.Context) (client.ContainerListResult, error) {
		return execution.defaultClient.ContainerList(observation, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("volume", name)})
	})
	if err != nil {
		return macRunnerFailure("inspect inner volume consumers "+name, err)
	}
	for _, consumer := range consumers.Items {
		_, owned := execution.fixtures.Containers[consumer.ID]
		if !owned || requireEmpty {
			reason := fmt.Sprintf("current consumer %s prevents exclusive cleanup", consumer.ID)
			execution.preserveFixture("volume", name, reason, true)
			return fmt.Errorf("preserve inner volume %s: %s", name, reason)
		}
		inspection, err := execution.inspect(ctx, consumer.ID)
		if err != nil {
			return err
		}
		if inspection.Config == nil || !maps.Equal(inspection.Config.Labels, execution.fixtures.Containers[consumer.ID].Labels) {
			execution.preserveFixture("volume", name, "consumer ownership changed", true)
			return fmt.Errorf("preserve inner volume %s after consumer ownership changed", name)
		}
	}
	return nil
}
