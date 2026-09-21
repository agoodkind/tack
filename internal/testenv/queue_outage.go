package testenv

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/moby/moby/client"
)

// StopQueueBroker kills the broker at index, the way a guest running one
// broker dies. The name that broker advertises keeps resolving to an address
// that refuses connections.
func StopQueueBroker(t T, index int) {
	t.Helper()
	name := queueBrokerContainer(t, index)
	if err := stopQueueBroker(t.Context(), name); err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
}

// StartQueueBroker starts the stopped broker at index and returns once the
// cluster reports every broker again.
func StartQueueBroker(t T, index int) {
	t.Helper()
	bootstrap := Queue(t)
	name := queueBrokerContainer(t, index)
	if err := startQueueBroker(t.Context(), name, bootstrap); err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
}

// queueBrokerContainer returns the container name of the broker at index. It
// provisions the cluster first because an index means nothing until the
// brokers exist.
func queueBrokerContainer(t T, index int) string {
	t.Helper()
	Queue(t)
	if index < 0 || index >= len(queueBrokerContainers) {
		_, _ = fmt.Fprintf(t.Output(), "testenv: broker %d is outside a cluster of %d\n", index, len(queueBrokerContainers))
		t.FailNow()
		return ""
	}
	return queueBrokerContainers[index]
}

// stopQueueBroker kills the container of one broker.
func stopQueueBroker(ctx context.Context, containerName string) error {
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	timeout := queueStopSeconds
	if _, err := cli.ContainerStop(ctx, containerName, client.ContainerStopOptions{Signal: "", Timeout: &timeout}); err != nil {
		slog.ErrorContext(ctx, "testenv.queue.stop_failed", slog.String("err", err.Error()))
		return fmt.Errorf("stop container %s: %w", containerName, err)
	}
	slog.InfoContext(ctx, "testenv.queue.stopped", slog.String("container", containerName))
	return nil
}

// startQueueBroker restarts the container of one broker. provisionTimeout
// bounds the wait for a whole cluster.
func startQueueBroker(ctx context.Context, containerName, bootstrap string) error {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.ContainerStart(ctx, containerName, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.queue.start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("start container %s: %w", containerName, err)
	}
	if err := waitForQueue(ctx, bootstrap); err != nil {
		return err
	}
	slog.InfoContext(ctx, "testenv.queue.started", slog.String("container", containerName))
	return nil
}
