package testenv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var errMacRunnerSocketRewrite = errors.New("local Docker Desktop rewrote the approved socket source")

func (execution *macRunnerExecution) request() client.ContainerCreateOptions {
	service := execution.prepared.service
	environment := make([]string, 0, len(service.Environment))
	for name, value := range service.Environment {
		environment = append(environment, name+"="+*value)
	}
	slices.Sort(environment)
	mounts := make([]mount.Mount, 0, len(service.Volumes))
	for _, volume := range service.Volumes {
		mounts = append(mounts, mount.Mount{Type: mount.Type(volume.Type), Source: volume.Source, Target: volume.Target})
	}
	return client.ContainerCreateOptions{
		Name: execution.name,
		Config: &container.Config{
			Image: service.Image, Entrypoint: slices.Clone([]string(service.Entrypoint)),
			Cmd: slices.Clone([]string(service.Command)), WorkingDir: service.WorkingDir,
			Env: environment, Labels: maps.Clone(execution.labels), AttachStdout: true, AttachStderr: true,
		},
		HostConfig: &container.HostConfig{Mounts: mounts, NetworkMode: container.NetworkMode(execution.prepared.project.Networks["default"].Name)},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			execution.prepared.project.Networks["default"].Name: {NetworkID: execution.networkID},
		}},
	}
}

func (execution *macRunnerExecution) create(ctx context.Context) error {
	request := execution.request()
	writeMacRunnerJSON(execution, "runner-request.json", request)
	writeMacRunnerJSON(execution, "refused-runners.json", execution.refused)
	for attempt := 1; attempt <= 5; attempt++ {
		created, err := macObserve(ctx, func(observation context.Context) (client.ContainerCreateResult, error) {
			return execution.defaultClient.ContainerCreate(observation, request)
		})
		execution.operation("container create", created.ID, err)
		if err != nil {
			recoveryErr := execution.recoverContainer(context.WithoutCancel(ctx))
			return macRunnerErrors("recover runner creation", macRunnerFailure(fmt.Sprintf("create runner attempt %d", attempt), err), recoveryErr)
		}
		execution.createdIDs = append(execution.createdIDs, created.ID)
		inspection, err := execution.inspect(ctx, created.ID)
		if err != nil {
			return err
		}
		err = execution.verify(inspection)
		if err == nil {
			execution.acceptedID = created.ID
			writeMacRunnerJSON(execution, "outer-created.json", inspection)
			execution.write("outer-id.txt", created.ID+"\n")
			writeMacRunnerJSON(execution, "runner-create-summary.json", macRunnerCreateSummary{Attempts: attempt, AcceptedID: created.ID, SocketRefusals: len(execution.refused)})
			return macRunnerErrors("record accepted runner", execution.evidenceErrors...)
		}
		refusal := macRunnerRefusal{ID: created.ID, Attempt: attempt, Reason: err.Error(), Inspection: inspection}
		execution.refused = append(execution.refused, refusal)
		writeMacRunnerJSON(execution, "refusal-"+strconv.Itoa(attempt)+".json", refusal)
		writeMacRunnerJSON(execution, "refused-runners.json", execution.refused)
		writeMacRunnerJSON(execution, "runner-create-summary.json", macRunnerCreateSummary{Attempts: attempt, AcceptedID: "", SocketRefusals: len(execution.refused)})
		if !errors.Is(err, errMacRunnerSocketRewrite) {
			return err
		}
		if cleanupErr := execution.removeRunner(ctx, created.ID); cleanupErr != nil {
			return macRunnerErrors("remove refused runner", err, cleanupErr)
		}
		if attempt == 5 {
			return macRunnerFailure("refuse runner after five socket rewrites", err)
		}
	}
	return errors.New("runner creation did not produce an accepted container")
}

func (execution *macRunnerExecution) inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	result, err := macObserve(ctx, func(observation context.Context) (client.ContainerInspectResult, error) {
		return execution.defaultClient.ContainerInspect(observation, id, client.ContainerInspectOptions{})
	})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return container.InspectResponse{}, err
		}
		return container.InspectResponse{}, macRunnerFailure("inspect runner "+id, err)
	}
	return result.Container, nil
}

func (execution *macRunnerExecution) recoverContainer(ctx context.Context) error {
	listed, err := macObserve(ctx, func(observation context.Context) (client.ContainerListResult, error) {
		return execution.defaultClient.ContainerList(observation, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("name", execution.name)})
	})
	if err != nil {
		execution.unknownResources = append(execution.unknownResources, "container name "+execution.name)
		return macRunnerFailure("recover partial runner creation", err)
	}
	var matches []string
	for _, item := range listed.Items {
		if !slices.Contains(item.Names, "/"+execution.name) {
			continue
		}
		if item.Labels[macRunnerLabel] == execution.name && item.Labels[macRunnerProjectLabel] == execution.prepared.project.Name && !execution.baseline.Containers[item.ID] {
			matches = append(matches, item.ID)
		} else {
			execution.unknownResources = append(execution.unknownResources, "container "+item.ID)
		}
	}
	if len(matches) > 1 {
		execution.unknownResources = append(execution.unknownResources, matches...)
		return errors.New("multiple containers match partial runner ownership; preserve unknown containers")
	}
	if len(matches) == 1 {
		execution.createdIDs = append(execution.createdIDs, matches[0])
	}
	if len(execution.unknownResources) != 0 {
		return errors.New("partial runner creation found resources without exact ownership; preserve unknown resources")
	}
	return nil
}
