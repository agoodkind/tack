//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureDisk(t *testing.T) {
	macRequireDiskCleanup(t, "Disk", 0)
}

func TestMacRunnerDockerFixtureDiskKilled(t *testing.T) {
	macRequireDiskCleanup(t, "DiskKilled", 1)
}

func macRequireDiskCleanup(t *testing.T, mode string, exitCode int) {
	t.Helper()
	options, docker, _ := macOwnershipFixture(t, mode)
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != exitCode {
		t.Errorf("disk fixture invocation: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	if mode == "DiskKilled" && len(summary.Fixtures.Containers) != 1 {
		t.Errorf("pre-consumer SIGKILL recorded %d containers, want only the disk helper", len(summary.Fixtures.Containers))
	}
	for id, fixture := range summary.Fixtures.Containers {
		if !fixture.Absent {
			t.Errorf("owned disk fixture lacks absence evidence: %s", id)
		}
		if _, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("owned disk fixture remains %s: %v", id, err)
		}
	}
	if !summary.Attribution || !summary.Cleanup || !summary.FullCleanup {
		t.Errorf("disk fixture cleanup did not complete: %+v", summary)
	}
	const diskVolumeCount = 2
	if len(summary.Fixtures.NamedVolumes) != diskVolumeCount {
		t.Fatalf("disk volume inventory = %v, want %d volumes", summary.Fixtures.NamedVolumes, diskVolumeCount)
	}
	for name := range summary.Fixtures.NamedVolumes {
		if !summary.Fixtures.Volumes[name] {
			t.Errorf("disk volume lacks ownership evidence: %s", name)
		}
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("owned disk volume remains %s: %v", name, err)
		}
	}
	if mode == "Disk" {
		contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		volumeCreates := make(map[string]bool)
		containerAttributes := false
		for _, line := range strings.Split(string(contents), "\n") {
			var event events.Message
			if json.Unmarshal([]byte(line), &event) != nil || event.Action != events.ActionCreate {
				continue
			}
			if event.Type == events.VolumeEventType && event.Actor.Attributes["driver"] == "local" {
				volumeCreates[event.Actor.ID] = true
			}
			if event.Type == events.ContainerEventType && event.Actor.Attributes["io.goodkind.tack.testenv.runner"] == summary.RunnerID && event.Actor.Attributes["image"] != "" {
				containerAttributes = true
			}
		}
		for name := range summary.Fixtures.NamedVolumes {
			if !volumeCreates[name] {
				t.Errorf("raw disk creation event with driver missing for %s", name)
			}
		}
		if !containerAttributes {
			t.Error("raw container creation event lacks its image attribute")
		}
	}
}
