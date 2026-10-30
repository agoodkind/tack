package testenv

import (
	"context"
	"fmt"
	"slices"

	"github.com/moby/moby/api/types/container"
)

func (execution *macRunnerExecution) recordCreatedRunner(id string) {
	execution.createdIDs = append(execution.createdIDs, id)
	execution.temporaryVolumes[id] = macRunnerTemporaryVolume{
		Name: "", IdentityError: "temporary volume identity has not been observed", Absent: false,
	}
}

func (execution *macRunnerExecution) recordTemporaryVolume(inspection container.InspectResponse) (string, error) {
	if err := execution.owned(inspection); err != nil {
		return "", err
	}
	name, err := execution.runnerTemporaryVolume(inspection)
	if err != nil {
		execution.temporaryVolumes[inspection.ID] = macRunnerTemporaryVolume{Name: "", IdentityError: err.Error(), Absent: false}
		return "", err
	}
	execution.temporaryVolumes[inspection.ID] = macRunnerTemporaryVolume{Name: name, IdentityError: "", Absent: false}
	execution.anonymousVolumes[name] = true
	execution.write("anonymous-volume.txt", macRunnerIdentities(execution.anonymousVolumes))
	return name, nil
}

func (execution *macRunnerExecution) cleanupMissingRunner(ctx context.Context, id string) error {
	volume := execution.temporaryVolumes[id]
	if volume.Name == "" {
		return execution.unknownTemporaryVolume(id)
	}
	return execution.removeVolume(ctx, volume.Name)
}

func (execution *macRunnerExecution) unknownTemporaryVolume(id string) error {
	resource := "temporary volume for container " + id
	if !slices.Contains(execution.unknownResources, resource) {
		execution.unknownResources = append(execution.unknownResources, resource)
	}
	return fmt.Errorf("cannot prove temporary-volume identity and absence for missing or refused runner %s", id)
}

func (execution *macRunnerExecution) verifyTemporaryVolumes() error {
	var failures []error
	for _, id := range execution.createdIDs {
		volume := execution.temporaryVolumes[id]
		if volume.Name == "" {
			failures = append(failures, execution.unknownTemporaryVolume(id))
			continue
		}
		if !volume.Absent {
			failures = append(failures, fmt.Errorf("temporary volume %s for runner %s lacks verified absence", volume.Name, id))
		}
	}
	return macRunnerErrors("verify runner temporary volumes", failures...)
}

func (execution *macRunnerExecution) recordVolumeAbsent(name string) {
	for id, volume := range execution.temporaryVolumes {
		if volume.Name != name {
			continue
		}
		volume.Absent = true
		execution.temporaryVolumes[id] = volume
	}
}
