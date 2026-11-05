//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"testing"

	"github.com/containerd/errdefs"
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
}
