package testenv

import (
	"context"
	"errors"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) finalizeFixtureEvidence(ctx context.Context, attribution, cleanup error) error {
	finalAttribution := execution.attributeFixtures(ctx)
	attribution = macRunnerErrors("verify final attribution", attribution, finalAttribution, execution.observerError)
	execution.attributionComplete = execution.acceptedID != "" && attribution == nil
	var failures []error
	for id, fixture := range execution.fixtures.Containers {
		_, err := execution.inspect(ctx, id)
		if !errdefs.IsNotFound(err) {
			failures = append(failures, macRunnerAbsence("final inner container", id, err))
			continue
		}
		fixture.Absent = true
		execution.fixtures.Containers[id] = fixture
	}
	for id, fixture := range execution.fixtures.Networks {
		_, err := macObserve(ctx, func(observation context.Context) (client.NetworkInspectResult, error) {
			return execution.defaultClient.NetworkInspect(observation, id, client.NetworkInspectOptions{})
		})
		if !errdefs.IsNotFound(err) {
			failures = append(failures, macRunnerAbsence("final inner network", id, err))
			continue
		}
		fixture.Absent = true
		execution.fixtures.Networks[id] = fixture
	}
	for name, anonymous := range execution.fixtures.Volumes {
		if !anonymous {
			continue
		}
		_, err := macObserve(ctx, func(observation context.Context) (client.VolumeInspectResult, error) {
			return execution.defaultClient.VolumeInspect(observation, name, client.VolumeInspectOptions{})
		})
		if !errdefs.IsNotFound(err) {
			failures = append(failures, macRunnerAbsence("final inner volume", name, err))
		}
	}
	cleanup = macRunnerErrors("verify final fixture cleanup", cleanup, macRunnerErrors("final fixture inventory", failures...))
	execution.fixtureCleanupComplete = execution.attributionComplete && cleanup == nil
	execution.status("fixture-attribution.exit", attribution)
	execution.status("fixture-cleanup.exit", cleanup)
	writeMacRunnerJSON(execution, "fixture-ids.json", execution.fixtures)
	writeMacRunnerJSON(execution, "inner-process-ids.json", execution.fixtures.Processes)
	writeMacRunnerJSON(execution, "unattributed-preserved-ids.json", execution.fixtures.Preserved)
	return macRunnerErrors("finalize fixture evidence", attribution, cleanup)
}

func macRunnerAbsence(kind, id string, err error) error {
	if err == nil {
		err = errors.New("resource still exists")
	}
	return macRunnerFailure("verify "+kind+" "+id+" absence", err)
}
