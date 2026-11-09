//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

// macRequireStopTimeout reads the accepted runner's inspection from the
// evidence directory and requires its stop timeout to match stop_grace_period.
func macRequireStopTimeout(t *testing.T, evidence string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(evidence, "outer-created.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inspection container.InspectResponse
	if err := json.Unmarshal(contents, &inspection); err != nil {
		t.Fatal(err)
	}
	expected := int(macStopGrace / time.Second)
	if inspection.Config == nil || inspection.Config.StopTimeout == nil || *inspection.Config.StopTimeout != expected {
		t.Errorf("runner did not apply stop timeout %d: %+v", expected, inspection.Config)
	}
}

// TestMacRunnerDockerFixtureSignals runs testenv.RunMacTests with the signal
// context that cmd/testenv uses. The test sends each signal to this process
// after the fixture reports readiness. The run must exit with status 130,
// remove every resource it owns, and keep the baseline marker and cache
// contents.
func TestMacRunnerDockerFixtureSignals(t *testing.T) {
	for _, signalCase := range []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "SIGINT", signal: syscall.SIGINT},
		{name: "SIGTERM", signal: syscall.SIGTERM},
	} {
		t.Run(signalCase.name, func(t *testing.T) {
			options, docker, project := macOwnershipFixture(t, "Wait")
			marker, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
				Name: project + "-baseline-marker", Config: &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}},
				HostConfig: &container.HostConfig{Mounts: []mount.Mount{
					{Type: mount.TypeVolume, Source: project + "_tack-test-go-cache", Target: "/root/.cache/go-build"},
					{Type: mount.TypeVolume, Source: project + "_tack-test-go-mod", Target: "/go/pkg/mod"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := docker.ContainerRemove(context.Background(), marker.ID, client.ContainerRemoveOptions{}); err != nil {
					t.Error(err)
				}
			})
			bounded, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
			defer cancel()
			signaled, stop := signal.NotifyContext(bounded, os.Interrupt, syscall.SIGTERM)
			defer stop()
			finished := make(chan testenv.MacTestResult, 1)
			go func() {
				result, runErr := testenv.RunMacTests(signaled, options)
				t.Logf("interrupted run error: %v", runErr)
				finished <- result
			}()
			macOwnershipReady(t, options.EvidenceDirectory)
			if err := syscall.Kill(os.Getpid(), signalCase.signal); err != nil {
				t.Fatal(err)
			}
			result := <-finished
			if result.ExitCode != 130 {
				t.Errorf("signal %s produced exit code %d, want 130", signalCase.signal.String(), result.ExitCode)
			}
			summary := macOwnershipEvidence(t, options.EvidenceDirectory)
			macRequireOwnedAbsent(t, docker, summary)
			macRequireStopTimeout(t, options.EvidenceDirectory)
			if !summary.FullCleanup || summary.ExitCode != 130 {
				t.Errorf("signal evidence did not prove complete fixture cleanup: %+v", summary)
			}
			if _, err := docker.ContainerInspect(t.Context(), marker.ID, client.ContainerInspectOptions{}); err != nil {
				t.Errorf("signal removed baseline marker: %v", err)
			}
			macCacheMarker(t, docker, marker.ID, "/root/.cache/go-build/mac-runner-marker")
			macCacheMarker(t, docker, marker.ID, "/go/pkg/mod/mac-runner-marker")
		})
	}
}
