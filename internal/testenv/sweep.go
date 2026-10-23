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

// removeEngines removes the testenv containers that selected chooses, then
// the testenv networks it chooses. It detaches each network owner's runner
// container before removing that network and returns the number of removed
// containers.
func removeEngines(ctx context.Context, cli *client.Client, self processOwner, selected removalTest) (int, error) {
	managed := client.Filters{}.Add("label", managedLabel)
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: managed})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.list_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("list test engines: %w", err)
	}
	var containers, names []string
	for _, item := range listed.Items {
		remove, err := selected(ctx, cli, self, item.Labels)
		if err != nil {
			return 0, err
		}
		if remove {
			containers = append(containers, item.ID)
			names = append(names, strings.Join(item.Names, ","))
		}
	}
	if err := removeContainersWith(ctx, cli, containers); err != nil {
		return 0, err
	}
	networks, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: managed})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.sweep.list_failed", slog.String("err", err.Error()))
		return len(containers), fmt.Errorf("list test networks: %w", err)
	}
	var failures []error
	var removedNetworks []string
	for _, item := range networks.Items {
		remove, err := selected(ctx, cli, self, item.Labels)
		if err != nil {
			return len(containers), err
		}
		if remove {
			removedNetworks = append(removedNetworks, item.Name)
			failures = append(failures, removeLedgerClusterNetwork(ctx, cli, item.Name, item.Labels[runnerLabel]))
		}
	}
	if len(names)+len(removedNetworks) > 0 {
		slog.InfoContext(ctx, "testenv.sweep.removed", slog.String("containers", strings.Join(names, " ")),
			slog.String("networks", strings.Join(removedNetworks, " ")))
	}
	return len(containers), errors.Join(failures...)
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
