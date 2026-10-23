package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

// removalTest decides from a container's or network's labels whether
// removeEngines removes it.
type removalTest func(ctx context.Context, cli *client.Client, self processOwner, labels map[string]string) (bool, error)

// engineSelection stores the containers and networks that removeEngines removes
// and the container names for its log entry.
type engineSelection struct {
	containers []string
	names      []string
	networks   []selectedNetwork
}

// selectedNetwork records a network that removeEngines removes and the runner
// container ID of the process that created it.
type selectedNetwork struct {
	name   string
	runner string
}

// removeEngines checks all testenv containers and networks before any removal
// and returns an error if a list or owner check fails.
// It removes selected containers, then removes each selected network after
// detaching its owner's runner container.
// It returns the number of containers it removes.
func removeEngines(ctx context.Context, cli *client.Client, self processOwner, selected removalTest) (int, error) {
	selection, err := selectEngines(ctx, cli, self, selected)
	if err != nil {
		return 0, err
	}
	if err := removeContainersWith(ctx, cli, selection.containers); err != nil {
		return 0, err
	}
	var failures []error
	networkNames := make([]string, 0, len(selection.networks))
	for _, item := range selection.networks {
		networkNames = append(networkNames, item.name)
		failures = append(failures, removeLedgerClusterNetwork(ctx, cli, item.name, item.runner))
	}
	if len(selection.names)+len(networkNames) > 0 {
		slog.InfoContext(ctx, "testenv.sweep.removed", slog.String("containers", strings.Join(selection.names, " ")),
			slog.String("networks", strings.Join(networkNames, " ")))
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.network_remove_failed", slog.String("err", err.Error()))
		return len(selection.containers), err
	}
	return len(selection.containers), nil
}

// selectEngines lists testenv containers and networks and returns those that
// selected chooses.
func selectEngines(ctx context.Context, cli *client.Client, self processOwner, selected removalTest) (engineSelection, error) {
	selection := engineSelection{containers: nil, names: nil, networks: nil}
	managed := client.Filters{}.Add("label", managedLabel)
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: managed})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.list_failed", slog.String("err", err.Error()))
		return selection, fmt.Errorf("list test engines: %w", err)
	}
	for _, item := range listed.Items {
		remove, err := selected(ctx, cli, self, item.Labels)
		if err != nil {
			return selection, err
		}
		if remove {
			selection.containers = append(selection.containers, item.ID)
			selection.names = append(selection.names, strings.Join(item.Names, ","))
		}
	}
	networks, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: managed})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.list_failed", slog.String("err", err.Error()))
		return selection, fmt.Errorf("list test networks: %w", err)
	}
	for _, item := range networks.Items {
		remove, err := selected(ctx, cli, self, item.Labels)
		if err != nil {
			return selection, err
		}
		if remove {
			selection.networks = append(selection.networks, selectedNetwork{name: item.Name, runner: item.Labels[runnerLabel]})
		}
	}
	return selection, nil
}

// removeRun removes every container and network with this process's run label.
func removeRun(ctx context.Context, self processOwner) error {
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	_, err = removeEngines(ctx, cli, self, func(_ context.Context, _ *client.Client, owner processOwner, labels map[string]string) (bool, error) {
		return labels[runLabel] == owner.run, nil
	})
	return err
}

// removeForOperator selects operator engines and the engines of exited test
// processes for testenv down.
func removeForOperator(ctx context.Context, cli *client.Client, self processOwner, labels map[string]string) (bool, error) {
	if labels[operatorLabel] == "true" {
		return true, nil
	}
	return ownerExited(ctx, cli, self, labels)
}
