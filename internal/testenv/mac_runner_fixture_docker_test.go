//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureOwnership(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "All")
	baselineRunner, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{Name: project + "-exited-owner", Config: &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}}})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{Name: project + "-exited-fixture", Config: &container.Config{Image: options.Image, Labels: map[string]string{
		"tack.mac-runner-fixture": project, "io.goodkind.tack.testenv": "true", "io.goodkind.tack.testenv.runner": baselineRunner.ID,
		"io.goodkind.tack.testenv.process": "99999999", "io.goodkind.tack.testenv.run": project, "io.goodkind.tack.testenv.host": "exited-owner",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []string{baseline.ID, baselineRunner.ID} {
			if _, err := docker.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true}); err != nil {
				t.Error(err)
			}
		}
	})
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 0 {
		t.Errorf("owned fixture invocation: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if !summary.Attribution || !summary.Cleanup || !summary.FullCleanup {
		t.Errorf("fixture cleanup did not complete: %+v", summary)
	}
	for name := range summary.Fixtures.Volumes {
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); err == nil {
			t.Errorf("owned anonymous volume remains %s", name)
		}
	}
	if _, err := docker.ContainerInspect(t.Context(), baseline.ID, client.ContainerInspectOptions{}); err != nil {
		t.Errorf("native invocation swept preexisting exited fixture %s: %v", baseline.ID, err)
	}
}

func TestMacRunnerDockerFixtureUnknown(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "Unknown")
	macOwnershipRecovery(t, docker, options.EvidenceDirectory)
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || result.ExitCode != 1 || !strings.Contains(err.Error(), "lacks exact invocation attribution") {
		t.Errorf("unknown fixture did not fail attribution: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	if summary.Attribution || summary.FullCleanup {
		t.Fatal("unknown fixture reported complete attribution")
	}
	count := 0
	for _, preserved := range summary.Fixtures.Preserved {
		if preserved.Kind != "container" || !preserved.Incomplete {
			continue
		}
		count++
		if _, err := docker.ContainerInspect(t.Context(), preserved.ID, client.ContainerInspectOptions{}); err != nil {
			t.Errorf("unknown container disappeared %s: %v", preserved.ID, err)
		}
	}
	if count != 1 {
		t.Fatalf("expected the exact preserved unknown container: %+v", summary.Fixtures.Preserved)
	}
}

func TestMacRunnerDockerFixtureMutation(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "Mutation")
	marker, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Name: project + "-mutation-baseline", Config: &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-cache", Target: "/root/.cache/go-build"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-mod", Target: "/go/pkg/mod"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := docker.ContainerRemove(context.Background(), marker.ID, client.ContainerRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || result.ExitCode != 1 || !strings.Contains(err.Error(), "source hash changed") {
		t.Errorf("source mutation: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if !summary.FullCleanup {
		t.Errorf("source mutation prevented fixture cleanup: %+v", summary)
	}
	if _, err := docker.ContainerInspect(t.Context(), marker.ID, client.ContainerInspectOptions{}); err != nil {
		t.Errorf("source mutation removed baseline marker: %v", err)
	}
	macCacheMarker(t, docker, marker.ID, "/root/.cache/go-build/mac-runner-marker")
	macCacheMarker(t, docker, marker.ID, "/go/pkg/mod/mac-runner-marker")
}

func TestMacRunnerDockerFixtureAdmissionRefusals(t *testing.T) {
	for mode, reason := range map[string]string{"MissingAdmission": "open required native runner admission", "MismatchedAdmission": "does not match inspected runner identity"} {
		t.Run(mode, func(t *testing.T) {
			options, docker, _ := macOwnershipFixture(t, mode)
			result, err := testenv.RunMacTests(t.Context(), options)
			if err != nil || result.ExitCode != 1 {
				t.Errorf("admission refusal: result=%+v error=%v", result, err)
			}
			contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "runtime.log"))
			if err != nil || !strings.Contains(string(contents), reason) {
				t.Errorf("admission did not refuse for the required reason: %s: %v", contents, err)
			}
			summary := macOwnershipEvidence(t, options.EvidenceDirectory)
			if len(summary.Fixtures.Containers) != 0 {
				t.Error("refused native runner created inner fixtures")
			}
			macRuntimeEvidence(t, options, docker)
		})
	}
}
