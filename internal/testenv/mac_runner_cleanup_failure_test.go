//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerCleanupFailureEvidence(t *testing.T) {
	options, docker, project := macDockerFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerSignal$", "-count=1", "-timeout=3m", "-tags=macrunnerfixture,runtimeaudit"}
	type completion struct {
		result testenv.MacTestResult
		err    error
	}
	completed := make(chan completion, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	received := false
	t.Cleanup(func() {
		if received {
			return
		}
		cancel()
		select {
		case <-completed:
		case <-time.After(2 * time.Minute):
			t.Error("cleanup failure runner did not finish after cancellation")
		}
	})
	go func() { result, err := testenv.RunMacTests(ctx, options); completed <- completion{result, err} }()
	id := macWaitFixtureReady(t, options.EvidenceDirectory)
	inspection, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	configuration := inspection.Container.Config
	if configuration == nil || configuration.Labels["com.docker.compose.project"] != project || configuration.Labels["io.goodkind.tack.mac-runner.root"] != options.Root {
		t.Fatal("runner temporary volume lacks exact fixture ownership")
	}
	var volume string
	for _, point := range inspection.Container.Mounts {
		if point.Destination == "/tmp/tack-test" && point.Type == mount.TypeVolume {
			volume = point.Name
		}
	}
	if volume == "" || volume == project+"_tack-test-go-cache" || volume == project+"_tack-test-go-mod" {
		t.Fatal("cleanup failure fixture requires a distinct temporary volume")
	}
	consumer, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Name:       project + "-temporary-consumer",
		Config:     &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/fixture-shared"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, err := docker.ContainerInspect(context.Background(), consumer.ID, client.ContainerInspectOptions{})
		if err != nil || current.Container.Config.Labels["tack.mac-runner-fixture"] != project {
			t.Errorf("refuse changed cleanup consumer %s: %v", consumer.ID, err)
			return
		}
		if _, err := docker.ContainerRemove(context.Background(), consumer.ID, client.ContainerRemoveOptions{}); err != nil {
			t.Error(err)
			return
		}
		if _, err := docker.VolumeRemove(context.Background(), volume, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	cancel()
	finished := <-completed
	received = true
	if finished.err == nil || !strings.Contains(finished.err.Error(), volume) || finished.result.ExitCode != 130 {
		t.Fatalf("temporary volume refusal: result=%+v error=%v", finished.result, finished.err)
	}
	macRequireStageFailure(t, options.EvidenceDirectory, "cleanup", volume)
	if macRead(t, options.EvidenceDirectory, "source-hash-verification.exit") != "0\n" {
		t.Fatal("cleanup failure prevented final source verification")
	}
	if _, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("cleanup failure preserved the owned outer runner: %v", err)
	}
	if _, err := docker.ContainerInspect(t.Context(), consumer.ID, client.ContainerInspectOptions{}); err != nil {
		t.Fatalf("cleanup failure removed the independent consumer: %v", err)
	}
	if _, err := docker.VolumeInspect(t.Context(), volume, client.VolumeInspectOptions{}); err != nil {
		t.Fatalf("cleanup failure removed the shared temporary volume: %v", err)
	}
}
