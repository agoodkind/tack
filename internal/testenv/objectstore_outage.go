package testenv

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/moby/moby/client"
)

// objectStoreStopSeconds is how long a stop waits for the engine to exit
// before killing it.
const objectStoreStopSeconds = 5

// StopObjectStore stops the engine in containerName the way a guest stops:
// its address stops answering, and its buckets stay on its volume for
// [StartObjectStore]. Docker may hand the stopped engine's address to another
// container, so a test must not rely on it staying silent for long.
func StopObjectStore(t T, containerName string) {
	t.Helper()
	skipWhenShort(t)
	if err := stopObjectStore(t.Context(), containerName); err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
}

// StartObjectStore starts the stopped engine in containerName and returns
// its endpoint once it stores objects again. The endpoint may differ from the
// one before the stop, because Docker assigns the address anew.
func StartObjectStore(t T, containerName string) string {
	t.Helper()
	skipWhenShort(t)
	endpoint, err := startObjectStore(t.Context(), containerName)
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
	return endpoint
}

// stopObjectStore stops the engine's container.
func stopObjectStore(ctx context.Context, containerName string) error {
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	timeout := objectStoreStopSeconds
	if _, err := cli.ContainerStop(ctx, containerName, client.ContainerStopOptions{Signal: "", Timeout: &timeout}); err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.stop_failed", slog.String("err", err.Error()))
		return fmt.Errorf("stop container %s: %w", containerName, err)
	}
	slog.InfoContext(ctx, "testenv.objectstore.stopped", slog.String("container", containerName))
	return nil
}

// startObjectStore starts the engine's container and waits, bounded by
// provisionTimeout, until it stores an object.
func startObjectStore(ctx context.Context, containerName string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.ContainerStart(ctx, containerName, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.start_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("start container %s: %w", containerName, err)
	}
	access, err := waitForObjectStore(ctx, cli, containerName)
	if err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.objectstore.started",
		slog.String("container", containerName), slog.String("endpoint", access.Endpoint))
	return access.Endpoint, nil
}
