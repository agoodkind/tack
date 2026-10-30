package testenv

import (
	"context"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) recordFixtureCreate(ctx context.Context, event events.Message) {
	if event.Action != events.ActionCreate {
		return
	}
	observed := macRunnerObservedCreate{Event: event, Container: nil, Network: nil, Error: ""}
	switch string(event.Type) {
	case string(events.ContainerEventType):
		inspection, err := execution.inspect(ctx, event.Actor.ID)
		if err != nil {
			observed.Error = err.Error()
		} else {
			observed.Container = &inspection
		}
	case string(events.NetworkEventType):
		inspection, err := macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
			return execution.defaultClient.NetworkInspect(observation, event.Actor.ID, client.NetworkInspectOptions{})
		})
		if err != nil {
			observed.Error = err.Error()
		} else {
			observed.Network = &inspection.Network
		}
	default:
		return
	}
	execution.eventSnapshot.Lock()
	execution.eventSnapshot.creates = append(execution.eventSnapshot.creates, observed)
	execution.eventSnapshot.Unlock()
}
