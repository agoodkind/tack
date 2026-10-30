//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestMacRunnerDockerRecoverTaskFixture(t *testing.T) {
	directory := os.Getenv("TACK_TESTENV_RECOVER_EVIDENCE")
	if directory == "" {
		t.Skip("requires an exact failed task fixture evidence directory")
	}
	docker, err := client.New(client.WithHost("unix://" + filepath.Join(os.Getenv("HOME"), ".docker/run/docker.sock")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = docker.Close() }()
	macRecoverOuterFixture(t, docker, directory)
}

func macRecoverOuterFixture(t *testing.T, docker *client.Client, directory string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, "outer-created.json"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var expected container.InspectResponse
	if err := json.Unmarshal(contents, &expected); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	current, err := docker.ContainerInspect(ctx, expected.ID, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	labels := current.Container.Config.Labels
	if current.Container.ID != expected.ID || current.Container.Name != expected.Name || labels["io.goodkind.tack.mac-runner.native"] != "true" || labels["com.docker.compose.project"] != expected.Config.Labels["com.docker.compose.project"] || labels["io.goodkind.tack.mac-runner.root"] != expected.Config.Labels["io.goodkind.tack.mac-runner.root"] {
		t.Fatal("refuse outer fixture recovery without exact recorded ownership")
	}
	var volume string
	for _, point := range current.Container.Mounts {
		if point.Type == mount.TypeVolume && point.Destination == "/tmp/tack-test" {
			volume = point.Name
		}
	}
	if _, err := docker.ContainerRemove(ctx, expected.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	t.Logf("removed exact failed outer fixture %s", expected.ID)
	if volume != "" {
		if _, err := docker.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	}
	networks, err := docker.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("label", "io.goodkind.tack.mac-runner="+labels["io.goodkind.tack.mac-runner"])})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range networks.Items {
		inspection, err := docker.NetworkInspect(ctx, item.ID, client.NetworkInspectOptions{})
		if err != nil {
			t.Error(err)
			continue
		}
		if inspection.Network.Labels["io.goodkind.tack.mac-runner"] != labels["io.goodkind.tack.mac-runner"] || len(inspection.Network.Containers) != 0 {
			t.Error("refuse occupied or changed fixture network")
			continue
		}
		if _, err := docker.NetworkRemove(ctx, item.ID, client.NetworkRemoveOptions{}); err != nil {
			t.Error(err)
		}
	}
	project := labels["com.docker.compose.project"]
	for _, suffix := range []string{"tack-test-go-cache", "tack-test-go-mod"} {
		name := project + "_" + suffix
		inspection, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if err != nil || inspection.Volume.Labels["tack.mac-runner-fixture"] != project {
			t.Errorf("refuse changed cache %s: %v", name, err)
			continue
		}
		if _, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	}
}
