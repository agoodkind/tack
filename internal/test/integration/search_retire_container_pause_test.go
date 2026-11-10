package integration_test

import (
	"context"
	"testing"

	"github.com/moby/moby/client"
)

// resumeContainerIfPaused resumes the container when Docker reports it as
// paused. A test that pauses the container registers this function as a
// cleanup. A hook may not run its resume action. Without the cleanup,
// the container remains paused and later tests on the engine hang.
// The state check prevents an error if the test already resumed the
// container.
func resumeContainerIfPaused(t *testing.T, container string) {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Errorf("open docker client: %v", err)
		return
	}
	defer func() { _ = docker.Close() }()
	inspected, err := docker.ContainerInspect(context.Background(), container, client.ContainerInspectOptions{})
	if err != nil {
		t.Errorf("inspect container %s: %v", container, err)
		return
	}
	if inspected.Container.State.Paused {
		setContainerPaused(t, container, false)
	}
}
