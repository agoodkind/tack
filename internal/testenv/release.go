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

// Release removes every engine container and cluster-file directory this
// process created. A test binary calls it after m.Run in its TestMain;
// cmd/testenv calls it when a command fails partway.
func Release(ctx context.Context) error {
	owned.Lock()
	containers, directories := owned.containers, owned.directories
	owned.containers, owned.directories = nil, nil
	owned.Unlock()
	var failures []error
	for _, directory := range directories {
		if err := os.RemoveAll(directory); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", directory, err))
		}
	}
	if len(containers) > 0 {
		failures = append(failures, removeContainers(ctx, containers))
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.release_failed", slog.String("err", err.Error()))
		return fmt.Errorf("release test engines: %w", err)
	}
	return nil
}

// Down removes every engine container any process or cmd/testenv started on
// this daemon, found by label, and then the engines' network.
func Down(ctx context.Context) (int, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cli.Close() }()
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", managedLabel),
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.down.list_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("list test engines: %w", err)
	}
	names := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		names = append(names, item.ID)
	}
	if err := removeContainersWith(ctx, cli, names); err != nil {
		return 0, err
	}
	removeNetwork(ctx, cli)
	return len(names), nil
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
