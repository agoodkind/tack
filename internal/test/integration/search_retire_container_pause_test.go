//go:build runtimeaudit

package integration_test

import (
	"context"
	"testing"

	"github.com/moby/moby/client"
)

// resumeContainerIfPaused resumes the container when Docker reports it
// as paused. A hook may not run its resume action. The container then
// remains paused, and later tests on the shared engine hang.
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
