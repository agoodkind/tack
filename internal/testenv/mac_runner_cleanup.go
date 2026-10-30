package testenv

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) cleanup(ctx context.Context) error {
	var failures []error
	for _, id := range execution.createdIDs {
		failures = append(failures, execution.removeRunner(ctx, id))
	}
	for name := range execution.anonymousVolumes {
		failures = append(failures, execution.removeVolume(ctx, name))
	}
	if execution.networkCreated {
		failures = append(failures, execution.removeNetwork(ctx))
	}
	if execution.baselineReady {
		failures = append(failures, execution.verifyPreserved(ctx))
	}
	return macRunnerErrors("clean outer runner resources", failures...)
}

func (execution *macRunnerExecution) removeRunner(ctx context.Context, id string) error {
	inspection, err := execution.inspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !slices.Contains(execution.createdIDs, id) {
		return fmt.Errorf("refuse unrecorded runner %s", id)
	}
	if err := execution.owned(inspection); err != nil {
		return err
	}
	temporary, err := execution.runnerTemporaryVolume(inspection)
	if err != nil {
		return err
	}
	execution.anonymousVolumes[temporary] = true
	execution.write("anonymous-volume.txt", macRunnerIdentities(execution.anonymousVolumes))
	stopErr := execution.stopRunner(ctx, inspection)
	_, removeErr := macObserve(ctx, func(observation context.Context) (client.ContainerRemoveResult, error) {
		return execution.defaultClient.ContainerRemove(observation, id, client.ContainerRemoveOptions{Force: true})
	})
	execution.operation("container remove", id, removeErr)
	if removeErr != nil {
		return macRunnerErrors("remove owned runner", stopErr, macRunnerFailure("remove owned runner "+id, removeErr))
	}
	_, err = execution.inspect(ctx, id)
	if !errdefs.IsNotFound(err) {
		if err == nil {
			err = errors.New("container still exists")
		}
		return macRunnerErrors("verify removed runner", stopErr, macRunnerFailure("verify runner "+id+" absence", err))
	}
	return macRunnerErrors("remove runner temporary volume", stopErr, execution.removeVolume(ctx, temporary))
}

func (execution *macRunnerExecution) runnerTemporaryVolume(inspection container.InspectResponse) (string, error) {
	var name string
	for _, point := range inspection.Mounts {
		if point.Destination != "/tmp/tack-test" {
			continue
		}
		if name != "" {
			return "", errors.New("runner inspection repeats the temporary mount")
		}
		if err := execution.temporaryVolume(point); err != nil {
			return "", err
		}
		name = point.Name
	}
	if name == "" {
		return "", errors.New("runner inspection did not report its temporary volume; preserve runner")
	}
	return name, nil
}

func (execution *macRunnerExecution) stopRunner(ctx context.Context, inspection container.InspectResponse) error {
	if inspection.State == nil {
		return fmt.Errorf("runner %s did not report state", inspection.ID)
	}
	if !inspection.State.Running {
		return nil
	}
	seconds := 10
	_, stopErr := macObserve(ctx, func(observation context.Context) (client.ContainerStopResult, error) {
		return execution.rawClient.ContainerStop(observation, inspection.ID, client.ContainerStopOptions{Timeout: &seconds})
	})
	execution.operation("container stop", inspection.ID, stopErr)
	final, inspectErr := execution.inspect(ctx, inspection.ID)
	if inspectErr != nil {
		return macRunnerErrors("inspect stopped runner", stopErr, inspectErr)
	}
	if final.State == nil || final.State.Running {
		return macRunnerErrors("stop runner", stopErr, fmt.Errorf("runner %s did not stop within 10 seconds", inspection.ID))
	}
	return stopErr
}
