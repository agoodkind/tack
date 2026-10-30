package testenv

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) prepareNetwork(ctx context.Context) error {
	configuration, present := execution.prepared.project.Networks["default"]
	_, serviceUsesDefault := execution.prepared.service.Networks["default"]
	if !present || configuration.Name == "" || len(execution.prepared.service.Networks) != 1 || !serviceUsesDefault {
		return errors.New("tests service must use only the loaded project's default network")
	}
	listed, err := macObserve(ctx, func(observation context.Context) (client.NetworkListResult, error) {
		return execution.defaultClient.NetworkList(observation, client.NetworkListOptions{
			Filters: client.Filters{}.Add("name", configuration.Name),
		})
	})
	if err != nil {
		return macRunnerFailure("find default network "+configuration.Name, err)
	}
	for _, item := range listed.Items {
		if item.Name != configuration.Name {
			continue
		}
		if execution.networkID != "" {
			return fmt.Errorf("multiple default networks match %s", configuration.Name)
		}
		if !execution.baseline.Networks[item.ID] {
			return fmt.Errorf("default network %s appeared after baseline", item.ID)
		}
		execution.networkID = item.ID
	}
	if execution.networkID != "" {
		return nil
	}
	if configuration.External {
		return fmt.Errorf("required external default network %s does not exist", configuration.Name)
	}
	labels := maps.Clone(configuration.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[macRunnerProjectLabel] = execution.prepared.project.Name
	labels["com.docker.compose.network"] = "default"
	labels[macRunnerLabel] = execution.name
	ipam, err := macRunnerIPAM(configuration.Ipam)
	if err != nil {
		return err
	}
	created, createErr := macObserve(ctx, func(observation context.Context) (client.NetworkCreateResult, error) {
		return execution.defaultClient.NetworkCreate(observation, configuration.Name, client.NetworkCreateOptions{
			Driver: configuration.Driver, Options: configuration.DriverOpts, Labels: labels,
			EnableIPv4: configuration.EnableIPv4, EnableIPv6: configuration.EnableIPv6,
			Internal: configuration.Internal, Attachable: configuration.Attachable, IPAM: ipam,
		})
	})
	execution.operation("network create", created.ID, createErr)
	if createErr != nil {
		recoveryErr := execution.recoverNetwork(context.WithoutCancel(ctx), configuration.Name)
		return macRunnerErrors("recover default network creation", macRunnerFailure("create default network "+configuration.Name, createErr), recoveryErr)
	}
	execution.networkID, execution.networkCreated = created.ID, true
	return nil
}

func (execution *macRunnerExecution) recoverNetwork(ctx context.Context, name string) error {
	listed, err := macObserve(ctx, func(observation context.Context) (client.NetworkListResult, error) {
		return execution.defaultClient.NetworkList(observation, client.NetworkListOptions{Filters: client.Filters{}.Add("name", name)})
	})
	if err != nil {
		execution.unknownResources = append(execution.unknownResources, "network name "+name)
		return macRunnerFailure("recover partial default network", err)
	}
	var matches []string
	for _, item := range listed.Items {
		if item.Name != name {
			continue
		}
		if item.Labels[macRunnerProjectLabel] == execution.prepared.project.Name && item.Labels[macRunnerLabel] == execution.name && !execution.baseline.Networks[item.ID] {
			matches = append(matches, item.ID)
		} else {
			execution.unknownResources = append(execution.unknownResources, "network "+item.ID)
		}
	}
	if len(matches) > 1 {
		execution.unknownResources = append(execution.unknownResources, matches...)
		return errors.New("multiple networks match partial default-network ownership; preserve unknown networks")
	}
	if len(matches) == 1 {
		execution.networkID, execution.networkCreated = matches[0], true
	}
	if len(execution.unknownResources) != 0 {
		return errors.New("partial network creation found resources without exact ownership; preserve unknown resources")
	}
	return nil
}
