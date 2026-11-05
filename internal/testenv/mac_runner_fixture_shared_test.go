//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureSharedVolume(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "Shared")
	type completion struct {
		result testenv.MacTestResult
		err    error
	}
	completed := make(chan completion, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	received := false
	t.Cleanup(func() {
		if !received {
			cancel()
			<-completed
		}
	})
	go func() { result, err := testenv.RunMacTests(ctx, options); completed <- completion{result, err} }()
	runner := macOwnershipReady(t, options.EvidenceDirectory)
	owned, err := docker.ContainerList(t.Context(), client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", "io.goodkind.tack.testenv.runner="+runner)})
	if err != nil {
		t.Fatal(err)
	}
	var volume string
	for _, item := range owned.Items {
		inspection, err := docker.ContainerInspect(t.Context(), item.ID, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, point := range inspection.Container.Mounts {
			if point.Destination == "/fixture-volume" && point.Type == mount.TypeVolume {
				volume = point.Name
			}
		}
	}
	if volume == "" {
		t.Fatal("fixture did not create its anonymous candidate volume")
	}
	consumer, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Name: project + "-unrelated-consumer", Config: &container.Config{Image: options.Image, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}, Labels: map[string]string{"tack.mac-runner-fixture": project}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/unrelated-volume"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := docker.ContainerRemove(context.Background(), consumer.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
		if _, err := docker.VolumeRemove(context.Background(), volume, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	if _, err := docker.ContainerStart(t.Context(), consumer.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	finished := <-completed
	received = true
	if finished.err == nil || finished.result.ExitCode != 130 {
		t.Errorf("shared volume cancellation: result=%+v error=%v", finished.result, finished.err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if summary.Cleanup || summary.FullCleanup {
		t.Error("shared volume incorrectly reported complete cleanup")
	}
	if !summary.Fixtures.Preserved["volume:"+volume].Incomplete {
		t.Errorf("shared volume lacks preserved evidence: %s", volume)
	}
	inspection, err := docker.ContainerInspect(t.Context(), consumer.ID, client.ContainerInspectOptions{})
	if err != nil || inspection.Container.State == nil || !inspection.Container.State.Running {
		t.Errorf("unrelated consumer did not survive: %s: %v", consumer.ID, err)
	}
	if _, err := docker.VolumeInspect(t.Context(), volume, client.VolumeInspectOptions{}); errdefs.IsNotFound(err) || err != nil {
		t.Errorf("shared anonymous volume did not survive: %s: %v", volume, err)
	}
}
