package testenv

import (
	"context"
	"fmt"
	"maps"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) removeFixtureNetwork(ctx context.Context, id string) error {
	fixture, recorded := execution.fixtures.Networks[id]
	if !recorded || execution.baseline.Networks[id] || id == execution.networkID {
		return fmt.Errorf("refuse baseline or unrecorded fixture network %s", id)
	}
	inspection, err := macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
		return execution.defaultClient.NetworkInspect(observation, id, client.NetworkInspectOptions{})
	})
	if errdefs.IsNotFound(err) {
		fixture.Absent = true
		execution.fixtures.Networks[id] = fixture
		return nil
	}
	if err != nil {
		return macRunnerFailure("inspect fixture network "+id, err)
	}
	current := inspection.Network
	if current.ID != id || current.Name != fixture.Name || !execution.fixtureOwner(current.Labels) || !maps.Equal(current.Labels, fixture.Labels) {
		return fmt.Errorf("preserve fixture network %s after ownership changed", id)
	}
	if _, attached := current.Containers[execution.acceptedID]; attached {
		outer, err := execution.inspect(ctx, execution.acceptedID)
		if err != nil {
			return err
		}
		if err := execution.owned(outer); err != nil {
			return err
		}
		if outer.State == nil || outer.State.Running {
			return fmt.Errorf("preserve fixture network %s while outer runner runs", id)
		}
		_, err = macObserve(ctx, func(observation context.Context) (client.NetworkDisconnectResult, error) {
			return execution.defaultClient.NetworkDisconnect(observation, id, client.NetworkDisconnectOptions{Container: execution.acceptedID})
		})
		execution.operation("fixture network disconnect", id, err)
		if err != nil {
			return macRunnerFailure("detach stopped runner from fixture network "+id, err)
		}
	}
	inspection, err = macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
		return execution.defaultClient.NetworkInspect(observation, id, client.NetworkInspectOptions{})
	})
	if err != nil {
		return err
	}
	if len(inspection.Network.Containers) != 0 || !execution.fixtureOwner(inspection.Network.Labels) {
		return fmt.Errorf("preserve fixture network %s with current endpoints or changed ownership", id)
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.NetworkRemoveResult, error) {
		return execution.defaultClient.NetworkRemove(observation, id, client.NetworkRemoveOptions{})
	})
	execution.operation("fixture network remove", id, err)
	if err != nil {
		return macRunnerFailure("remove fixture network "+id, err)
	}
	_, err = macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
		return execution.defaultClient.NetworkInspect(observation, id, client.NetworkInspectOptions{})
	})
	if !errdefs.IsNotFound(err) {
		return macRunnerAbsence("fixture network", id, err)
	}
	fixture.Absent = true
	execution.fixtures.Networks[id] = fixture
	return nil
}
