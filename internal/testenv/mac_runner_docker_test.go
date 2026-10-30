//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

type macRuntimeSummary struct {
	RuntimeExit      *int            `json:"runtime_exit"`
	ExitCode         int             `json:"exit_code"`
	RunnerID         string          `json:"runner_id"`
	Started          bool            `json:"started"`
	AttachComplete   bool            `json:"attach_complete"`
	EventsComplete   bool            `json:"events_complete"`
	AnonymousVolumes map[string]bool `json:"anonymous_volumes"`
	CreatedIDs       []string        `json:"created_ids"`
	Baseline         struct {
		Containers map[string]bool `json:"containers"`
		Networks   map[string]bool `json:"networks"`
		Volumes    map[string]bool `json:"volumes"`
	} `json:"baseline"`
	Operations []struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	} `json:"operations"`
	Stages []struct {
		Name  string `json:"name"`
		Error string `json:"error"`
	} `json:"stages"`
}

func macDockerFixture(t *testing.T) (testenv.MacTestOptions, *client.Client, string) {
	t.Helper()
	image := os.Getenv("TACK_TESTENV_RUNNER_IMAGE")
	if image == "" {
		t.Fatal("set TACK_TESTENV_RUNNER_IMAGE to the required existing immutable image ID")
	}
	options, _ := macFixture(t)
	options.Image = image
	digest := sha256.Sum256([]byte(options.Root))
	project := fmt.Sprintf("tack-runner-fixture-%x", digest[:8])
	t.Setenv("COMPOSE_PROJECT_NAME", project)
	options.TestArguments = []string{"-run=^TestMacRunner(Flags|Skip|CacheMarker)$", "-count=2", "-timeout=45s", "-tags=macrunnerfixture,runtimeaudit", "-skip=^TestMacRunnerSkip$"}
	contents, err := os.ReadFile("testdata/macrunnerfixture/runner_test.go")
	if err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "internal/test/integration/example_test.go", string(contents))
	macWrite(t, options.Root, "go.mod", "module goodkind.io/tack\n\ngo 1.27\n")
	docker, err := client.New(client.WithHost("unix://" + filepath.Join(os.Getenv("HOME"), ".docker/run/docker.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	for _, suffix := range []string{"tack-test-go-cache", "tack-test-go-mod"} {
		name := project + "_" + suffix
		created, err := docker.VolumeCreate(t.Context(), client.VolumeCreateOptions{Name: name, Labels: map[string]string{"tack.mac-runner-fixture": project}})
		if err != nil || created.Volume.Name != name {
			t.Fatalf("create fixture cache %s: %v", name, err)
		}
		t.Cleanup(func() { macRemoveFixtureVolume(t, docker, name, project) })
	}
	if parent := os.Getenv("TACK_TESTENV_RUNNER_EVIDENCE_PARENT"); parent != "" {
		options.EvidenceDirectory = filepath.Join(parent, project)
	}
	return options, docker, project
}

func macRemoveFixtureVolume(t *testing.T, docker *client.Client, name, project string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inspection, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil || inspection.Volume.Labels["tack.mac-runner-fixture"] != project {
		t.Errorf("refuse fixture cache removal %s: %v", name, err)
		return
	}
	if _, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
		t.Error(err)
	}
}

func macRuntimeEvidence(t *testing.T, options testenv.MacTestOptions, docker *client.Client) macRuntimeSummary {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary macRuntimeSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	for _, operation := range summary.Operations {
		switch operation.Operation {
		case "container remove":
			if summary.Baseline.Containers[operation.ID] || !slices.Contains(summary.CreatedIDs, operation.ID) {
				t.Fatalf("container mutation lacks invocation ownership: %+v", operation)
			}
		case "volume remove":
			if summary.Baseline.Volumes[operation.ID] || !summary.AnonymousVolumes[operation.ID] {
				t.Fatalf("volume mutation lacks invocation ownership: %+v", operation)
			}
		case "network remove":
			if summary.Baseline.Networks[operation.ID] {
				t.Fatalf("network mutation selected a baseline identity: %+v", operation)
			}
		}
	}
	if summary.RunnerID != "" {
		_, err := docker.ContainerInspect(t.Context(), summary.RunnerID, client.ContainerInspectOptions{})
		if !errdefs.IsNotFound(err) {
			t.Fatalf("runner remains after finalization: %v", err)
		}
	}
	for volume := range summary.AnonymousVolumes {
		if _, err := docker.VolumeInspect(t.Context(), volume, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Fatalf("owned temporary volume remains: %s: %v", volume, err)
		}
	}
	entries, err := os.ReadDir(options.EvidenceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		information, err := entry.Info()
		if err != nil || information.Mode().Perm() != 0o600 {
			t.Fatalf("evidence permissions for %s: %v", entry.Name(), err)
		}
	}
	t.Logf("private evidence: %s", options.EvidenceDirectory)
	return summary
}

func TestMacRunnerDockerFlagsAndCleanup(t *testing.T) {
	options, docker, project := macDockerFixture(t)
	reusedNetwork := macReusedFixtureNetwork(t, docker, project)
	marker, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Name: project + "-baseline", Config: &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-cache", Target: "/root/.cache/go-build"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-mod", Target: "/go/pkg/mod"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := docker.ContainerRemove(context.Background(), marker.ID, client.ContainerRemoveOptions{})
		if err != nil {
			t.Error(err)
		}
	})
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 0 {
		t.Errorf("fixture runner: result=%+v error=%v", result, err)
	}
	summary := macRuntimeEvidence(t, options, docker)
	if summary.RuntimeExit == nil || *summary.RuntimeExit != 0 || !summary.AttachComplete || !summary.EventsComplete {
		t.Fatalf("incomplete runtime evidence: %+v", summary)
	}
	contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contents), "MAC_RUNNER_FLAGS") != 2 || strings.Count(string(contents), "MAC_RUNNER_CACHE_MARKER") != 2 || strings.Contains(string(contents), "=== RUN   TestMacRunnerSkip") || !strings.Contains(string(contents), "-test.timeout=45s") {
		t.Fatalf("fixture output did not prove exact selection, count, timeout and skip: %s", contents)
	}
	if _, err := docker.ContainerInspect(t.Context(), marker.ID, client.ContainerInspectOptions{}); err != nil {
		t.Fatalf("baseline marker disappeared: %v", err)
	}
	if _, err := docker.NetworkInspect(t.Context(), reusedNetwork, client.NetworkInspectOptions{}); err != nil {
		t.Fatalf("reused default network disappeared: %v", err)
	}
	macCacheMarker(t, docker, marker.ID, "/root/.cache/go-build/mac-runner-marker")
	macCacheMarker(t, docker, marker.ID, "/go/pkg/mod/mac-runner-marker")
}
