package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

const (
	macRunnerCreateEvidenceLimit = 16 << 20
	macRunnerInspectionLimit     = 4096
)

func (execution *macRunnerExecution) recordFixtureCreate(ctx context.Context, event events.Message) (bool, error) {
	if event.Action != events.ActionCreate {
		return false, nil
	}
	execution.eventSnapshot.Lock()
	runnerID := execution.eventSnapshot.runnerID
	execution.eventSnapshot.Unlock()
	observed := macRunnerObservedCreate{Event: event, Container: nil, Network: nil, Volume: nil, Error: ""}
	switch string(event.Type) {
	case string(events.ContainerEventType):
		eventRunner := event.Actor.Attributes[runnerLabel]
		if event.Actor.Attributes[managedLabel] != "true" || runnerID == "" || (eventRunner != "" && eventRunner != runnerID) {
			return false, nil
		}
		if err := execution.countFixtureInspection(); err != nil {
			return false, err
		}
		inspection, err := execution.inspect(ctx, event.Actor.ID)
		if err != nil {
			observed.Error = err.Error()
		} else {
			if inspection.Config == nil || inspection.Config.Labels[managedLabel] != "true" || (inspection.Config.Labels[runnerLabel] != "" && inspection.Config.Labels[runnerLabel] != runnerID) {
				return false, nil
			}
			observed.Container = macRunnerCompactContainer(inspection)
		}
	case string(events.NetworkEventType):
		if !strings.HasPrefix(event.Actor.Attributes["name"], "tack-testenv-") {
			return false, nil
		}
		if err := execution.countFixtureInspection(); err != nil {
			return false, err
		}
		inspection, err := macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
			return execution.defaultClient.NetworkInspect(observation, event.Actor.ID, client.NetworkInspectOptions{})
		})
		if err != nil {
			return false, macRunnerFailure("inspect created fixture network "+event.Actor.ID, err)
		}
		if runnerID == "" || inspection.Network.Labels[runnerLabel] != runnerID {
			return false, nil
		}
		observed.Network = &network.Inspect{ID: inspection.Network.ID, Name: inspection.Network.Name, Labels: inspection.Network.Labels}
	case string(events.VolumeEventType):
		recorded, err := execution.observeDiskCreate(ctx, event, runnerID, &observed)
		if err != nil {
			return false, err
		}
		if !recorded {
			return false, nil
		}
	default:
		return false, nil
	}
	observed.Event.Actor.Attributes = map[string]string{
		"name": event.Actor.Attributes["name"], runnerLabel: event.Actor.Attributes[runnerLabel], managedLabel: event.Actor.Attributes[managedLabel],
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return false, macRunnerFailure("encode fixture creation evidence", err)
	}
	execution.eventSnapshot.Lock()
	defer execution.eventSnapshot.Unlock()
	if execution.eventSnapshot.bytes+len(encoded) > macRunnerCreateEvidenceLimit {
		return false, errors.New("fixture creation evidence exceeds 16 MiB")
	}
	execution.eventSnapshot.bytes += len(encoded)
	execution.eventSnapshot.creates = append(execution.eventSnapshot.creates, observed)
	return true, nil
}

func macRunnerCompactContainer(inspection container.InspectResponse) *container.InspectResponse {
	var hostConfig *container.HostConfig
	if inspection.HostConfig != nil {
		hostConfig = &container.HostConfig{Binds: inspection.HostConfig.Binds, Mounts: inspection.HostConfig.Mounts}
	}
	return &container.InspectResponse{
		ID: inspection.ID, Name: inspection.Name, Mounts: inspection.Mounts,
		Config:     &container.Config{Labels: inspection.Config.Labels, Volumes: inspection.Config.Volumes},
		HostConfig: hostConfig,
	}
}

func (execution *macRunnerExecution) observeDiskCreate(ctx context.Context, event events.Message, runnerID string, observed *macRunnerObservedCreate) (bool, error) {
	if runnerID == "" || !strings.HasPrefix(event.Actor.ID, dataDiskPrefix) || (!strings.HasSuffix(event.Actor.ID, "-image") && !strings.HasSuffix(event.Actor.ID, "-data")) {
		return false, nil
	}
	if err := execution.countFixtureInspection(); err != nil {
		return false, err
	}
	inspection, err := macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
		return execution.defaultClient.VolumeInspect(observation, event.Actor.ID, client.VolumeInspectOptions{})
	})
	if err != nil {
		return false, macRunnerFailure("inspect created disk volume "+event.Actor.ID, err)
	}
	if inspection.Volume.Labels[managedLabel] != "true" || inspection.Volume.Labels[runnerLabel] != runnerID {
		return false, nil
	}
	observed.Volume = &volume.Volume{Name: inspection.Volume.Name, Driver: inspection.Volume.Driver, Labels: inspection.Volume.Labels, Options: inspection.Volume.Options, CreatedAt: inspection.Volume.CreatedAt}
	return true, nil
}

func (execution *macRunnerExecution) countFixtureInspection() error {
	execution.eventSnapshot.Lock()
	defer execution.eventSnapshot.Unlock()
	if execution.eventSnapshot.inspections >= macRunnerInspectionLimit {
		return errors.New("fixture event inspections exceed 4096")
	}
	execution.eventSnapshot.inspections++
	return nil
}
