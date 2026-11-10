package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// owned lists what this process created: the engine containers and the
// directories holding their cluster files.
var owned struct {
	sync.Mutex
	containers  []string
	directories []string
}

// own records a container this process created.
func own(name string) {
	owned.Lock()
	defer owned.Unlock()
	owned.containers = append(owned.containers, name)
}

// ownDirectory records a directory this process created.
func ownDirectory(path string) {
	owned.Lock()
	defer owned.Unlock()
	owned.directories = append(owned.directories, path)
}

// Release removes the cluster file directories this process created and
// every container and network with this process's run label. TestMain runs
// Release after m.Run, the signal handler runs Release on SIGINT or SIGTERM,
// and cmd/testenv runs Release when a command fails partway.
func Release(ctx context.Context) error {
	owned.Lock()
	directories := owned.directories
	owned.containers, owned.directories = nil, nil
	owned.Unlock()
	var failures []error
	for _, directory := range directories {
		if err := os.RemoveAll(directory); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", directory, err))
		}
	}
	if self, created := knownOwner(); created {
		failures = append(failures, removeRun(ctx, self))
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.release_failed", slog.String("err", err.Error()))
		return fmt.Errorf("release test engines: %w", err)
	}
	return nil
}

// Down removes the engines cmd/testenv started and the containers and
// networks of test processes that have exited, then removes the shared
// engine network if containers do not use it. It keeps engines of running
// test processes and engines without owner labels, and returns the number
// of removed containers.
func Down(ctx context.Context) (int, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cli.Close() }()
	self, err := currentOwner(ctx, cli)
	if err != nil {
		return 0, err
	}
	removed, err := removeEngines(ctx, cli, self, removeForOperator)
	if err != nil {
		return removed, err
	}
	removeNetwork(ctx, cli)
	return removed, nil
}

// removeContainers force-removes containers, with their anonymous volumes.
func removeContainers(ctx context.Context, names []string) error {
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	return removeContainersWith(ctx, cli, names)
}

// removeContainersWith force-removes containers through an open client. A
// container already gone counts as removed.
func removeContainersWith(ctx context.Context, cli *client.Client, names []string) error {
	var failures []error
	for _, name := range names {
		_, err := cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("remove container %s: %w", name, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.remove_failed", slog.String("err", err.Error()))
		return fmt.Errorf("remove test engines: %w", err)
	}
	return nil
}

// removeNetwork removes the engines' network. A network still in use, such
// as by the test runner container, stays; that costs nothing.
func removeNetwork(ctx context.Context, cli *client.Client) {
	_, err := cli.NetworkRemove(ctx, networkName, client.NetworkRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		slog.InfoContext(ctx, "testenv.down.network_kept", slog.String("err", err.Error()))
	}
}
