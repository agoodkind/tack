//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func macContainerInspectionID(request *http.Request) string {
	if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/json") {
		return ""
	}
	parts := strings.Split(request.URL.Path, "/")
	if len(parts) < 4 || parts[len(parts)-3] != "containers" {
		return ""
	}
	return parts[len(parts)-2]
}

func macRemoveUnloggedFixture(
	ctx context.Context,
	docker *client.Client,
	evidence string,
	id string,
	fixture *macMissingFixture,
) (bool, error) {
	inspection, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect test candidate: %w", err)
	}
	current := inspection.Container
	if current.Config == nil || current.Config.Labels["tack.mac-runner-unlogged"] != "true" {
		return false, nil
	}
	outerID, err := os.ReadFile(filepath.Join(evidence, "outer-id.txt"))
	if err != nil {
		return false, fmt.Errorf("read test runner identity: %w", err)
	}
	outer, err := docker.ContainerInspect(
		ctx,
		strings.TrimSpace(string(outerID)),
		client.ContainerInspectOptions{},
	)
	if err != nil || outer.Container.Config == nil ||
		current.Config.Labels["io.goodkind.tack.testenv.host"] != outer.Container.Config.Hostname ||
		current.Config.Labels["io.goodkind.tack.testenv"] != "true" ||
		current.Config.Labels["io.goodkind.tack.testenv.runner"] != "" ||
		!strings.HasPrefix(current.Name, "/tack-testenv-unlogged-") {
		return false, fmt.Errorf("refuse unverified test container %s: %v", id, err)
	}
	if current.HostConfig == nil || len(current.HostConfig.Mounts) != 1 ||
		current.HostConfig.Mounts[0].Type != mount.TypeVolume ||
		current.HostConfig.Mounts[0].Source != "" ||
		current.HostConfig.Mounts[0].Target != "/fixture-volume" {
		return false, fmt.Errorf("refuse test container %s without anonymous mount request", id)
	}
	var volumeName string
	for _, point := range current.Mounts {
		if point.Type == mount.TypeVolume && point.Destination == "/fixture-volume" {
			volumeName = point.Name
		}
	}
	if len(volumeName) != 64 {
		return false, fmt.Errorf("refuse invalid test volume identity for %s", id)
	}
	inspected, err := docker.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{})
	if err != nil {
		return false, fmt.Errorf("inspect test volume %s: %w", volumeName, err)
	}
	fixture.Lock()
	fixture.id, fixture.name, fixture.volume = id, strings.TrimPrefix(
		current.Name,
		"/",
	), inspected.Volume
	fixture.Unlock()
	if _, err := docker.ContainerRemove(
		ctx,
		id,
		client.ContainerRemoveOptions{Force: true},
	); err != nil {
		return false, fmt.Errorf("remove test container %s: %w", id, err)
	}
	return true, nil
}

func macRecoverMissingFixture(t *testing.T, docker *client.Client, fixture *macMissingFixture) {
	t.Helper()
	fixture.Lock()
	id, expected := fixture.id, fixture.volume
	fixture.Unlock()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := docker.ContainerInspect(
		ctx,
		id,
		client.ContainerInspectOptions{},
	); !errdefs.IsNotFound(err) {
		t.Errorf("refuse volume recovery while fixture container remains: %v", err)
		return
	}
	current, err := docker.VolumeInspect(ctx, expected.Name, client.VolumeInspectOptions{})
	if err != nil || current.Volume.Name != expected.Name ||
		current.Volume.Driver != expected.Driver ||
		current.Volume.CreatedAt != expected.CreatedAt ||
		!maps.Equal(current.Volume.Labels, expected.Labels) ||
		!maps.Equal(current.Volume.Options, expected.Options) {
		t.Errorf("refuse changed anonymous test volume %s: %v", expected.Name, err)
		return
	}
	consumers, err := docker.ContainerList(
		ctx,
		client.ContainerListOptions{
			All:     true,
			Filters: client.Filters{}.Add("volume", expected.Name),
		},
	)
	if err != nil || len(consumers.Items) != 0 {
		t.Errorf("refuse occupied anonymous test volume %s: %v", expected.Name, err)
		return
	}
	if _, err := docker.VolumeRemove(ctx, expected.Name, client.VolumeRemoveOptions{}); err != nil {
		t.Error(err)
	}
}
