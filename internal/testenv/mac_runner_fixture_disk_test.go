//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureDisk(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "Disk")
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 0 {
		t.Errorf("disk fixture invocation: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
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
