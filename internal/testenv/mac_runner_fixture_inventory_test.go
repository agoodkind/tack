//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

type macTaskInventory struct {
	CreatedIDs       []string        `json:"created_ids"`
	AnonymousVolumes map[string]bool `json:"anonymous_volumes"`
	NetworkID        string          `json:"network_id"`
	NetworkCreated   bool            `json:"network_created"`
	Fixtures         struct {
		Containers map[string]json.RawMessage `json:"containers"`
		Networks   map[string]json.RawMessage `json:"networks"`
		Volumes    map[string]bool            `json:"volume_candidates"`
	} `json:"fixtures"`
}

func TestMacRunnerDockerTaskResourceInventory(t *testing.T) {
	directories := os.Getenv("TACK_TESTENV_VERIFY_EVIDENCE")
	if directories == "" {
		t.Skip("requires exact task evidence directories for a read-only inventory")
	}
	image := os.Getenv("TACK_TESTENV_RUNNER_IMAGE")
	if image == "" {
		t.Fatal("require the existing immutable runner image ID")
	}
	docker, err := client.New(client.WithHost("unix://" + filepath.Join(os.Getenv("HOME"), ".docker/run/docker.sock")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = docker.Close() }()
	for _, directory := range strings.Split(directories, ",") {
		macVerifyTaskInventory(t, docker, directory)
	}
	inspection, err := docker.ImageInspect(t.Context(), image)
	if err != nil || inspection.ID != image {
		t.Fatalf("required immutable image identity changed: %v", err)
	}
	t.Logf("required immutable image remains %s", inspection.ID)
}

func macVerifyTaskInventory(t *testing.T, docker *client.Client, directory string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result macTaskInventory
	if err := json.Unmarshal(contents, &result); err != nil {
		t.Fatal(err)
	}
	for id := range result.Fixtures.Containers {
		result.CreatedIDs = append(result.CreatedIDs, id)
	}
	for _, id := range result.CreatedIDs {
		if _, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("recorded task container still exists %s: %v", id, err)
		}
		t.Logf("recorded task container absent %s", id)
	}
	for name, anonymous := range result.Fixtures.Volumes {
		if anonymous {
			result.AnonymousVolumes[name] = true
		}
	}
	for name := range result.AnonymousVolumes {
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("recorded task volume still exists %s: %v", name, err)
		}
		t.Logf("recorded task volume absent %s", name)
	}
	if result.NetworkCreated {
		result.Fixtures.Networks[result.NetworkID] = nil
	}
	for id := range result.Fixtures.Networks {
		if _, err := docker.NetworkInspect(t.Context(), id, client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("recorded task network still exists %s: %v", id, err)
		}
		t.Logf("recorded task network absent %s", id)
	}
}
