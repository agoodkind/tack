//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

// macBaselineRemoval starts the signal fixture, removes a baseline container
// after fixture readiness, and cancels the run. It returns the container ID,
// run error, and evidence directory.
func macBaselineRemoval(
	t *testing.T,
	labels func(project string) map[string]string,
) (string, error, string) {
	t.Helper()
	options, docker, project := macDockerFixture(t)
	options.TestArguments = []string{
		"-run=^TestMacRunnerSignal$",
		"-count=1",
		"-timeout=3m",
		"-tags=macrunnerfixture,runtimeaudit",
	}
	marker, err := docker.ContainerCreate(
		t.Context(),
		client.ContainerCreateOptions{
			Name:   project + "-removed-baseline",
			Config: &container.Config{Image: options.Image, Labels: labels(project)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(
			context.Background(),
			marker.ID,
			client.ContainerRemoveOptions{Force: true},
		)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, runErr := testenv.RunMacTests(ctx, options)
		finished <- runErr
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Minute):
			t.Error("the runner did not finish after cancellation")
		}
	})
	macWaitFixtureReady(t, options.EvidenceDirectory)
	if _, err := docker.ContainerRemove(
		t.Context(),
		marker.ID,
		client.ContainerRemoveOptions{Force: true},
	); err != nil {
		t.Fatal(err)
	}
	cancel()
	runErr := <-finished
	finished <- runErr
	return marker.ID, runErr, options.EvidenceDirectory
}

// TestMacRunnerDockerBaselineRemovedElsewhere checks that the result records
// a baseline container removed outside the project and that the removal does
// not produce a disappeared identity error.
func TestMacRunnerDockerBaselineRemovedElsewhere(t *testing.T) {
	id, runErr, directory := macBaselineRemoval(
		t,
		func(project string) map[string]string {
			return map[string]string{"tack.mac-runner-fixture": project}
		},
	)
	if runErr != nil && strings.Contains(runErr.Error(), "disappeared during invocation") {
		t.Errorf("a removal outside the project failed the run: %v", runErr)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		RemovedElsewhere []string `json:"baseline_removed_elsewhere"`
	}
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(summary.RemovedElsewhere, "container "+id) {
		t.Errorf("evidence omitted the removed container %s: %v", id, summary.RemovedElsewhere)
	}
}

// TestMacRunnerDockerBaselineRemovedInProject checks that removing a baseline
// container in the project produces an error identifying the container.
func TestMacRunnerDockerBaselineRemovedInProject(t *testing.T) {
	id, runErr, _ := macBaselineRemoval(
		t,
		func(project string) map[string]string {
			return map[string]string{
				"tack.mac-runner-fixture":    project,
				"com.docker.compose.project": project,
				"com.docker.compose.service": "other",
			}
		},
	)
	if runErr == nil ||
		!strings.Contains(
			runErr.Error(),
			"baseline container "+id+" disappeared during invocation",
		) {
		t.Errorf("a removal in the project returned %v", runErr)
	}
}
