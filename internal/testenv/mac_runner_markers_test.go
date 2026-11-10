//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"archive/tar"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func macReusedFixtureNetwork(t *testing.T, docker *client.Client, project string) string {
	t.Helper()
	created, err := docker.NetworkCreate(t.Context(), project+"_default", client.NetworkCreateOptions{
		Labels: map[string]string{"tack.mac-runner-fixture": project, "com.docker.compose.project": project},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, err := docker.NetworkInspect(context.Background(), created.ID, client.NetworkInspectOptions{})
		if err != nil || current.Network.Labels["tack.mac-runner-fixture"] != project || len(current.Network.Containers) != 0 {
			t.Errorf("refuse fixture network removal %s: %v", created.ID, err)
			return
		}
		if _, err := docker.NetworkRemove(context.Background(), created.ID, client.NetworkRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	return created.ID
}

func macCacheMarker(t *testing.T, docker *client.Client, id, path string) {
	t.Helper()
	archive, err := docker.CopyFromContainer(t.Context(), id, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Content.Close() }()
	reader := tar.NewReader(archive.Content)
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil || strings.TrimSpace(string(contents)) != "MAC_RUNNER_CACHE_MARKER" {
		t.Fatalf("cache marker did not survive outer cleanup: path=%s contents=%q error=%v", path, contents, err)
	}
}
