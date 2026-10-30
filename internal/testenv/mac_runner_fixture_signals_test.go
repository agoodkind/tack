//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestMacRunnerDockerFixtureCommandSignals(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(strconv.Itoa(int(signal)), func(t *testing.T) {
			options, docker, project := macOwnershipFixture(t, "Wait")
			marker, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
				Name: project + "-baseline-marker", Config: &container.Config{Image: options.Image, Labels: map[string]string{"tack.mac-runner-fixture": project}},
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
			binary, err := filepath.Abs("../../dist/testenv")
			if err != nil {
				t.Fatal(err)
			}
			arguments := []string{"run-mac", "--root", options.Root, "--image", options.Image, "--evidence-dir", options.EvidenceDirectory, "--"}
			arguments = append(arguments, options.TestArguments...)
			bounded, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
			defer cancel()
			command := exec.CommandContext(bounded, binary, arguments...)
			command.Env, command.Stdout, command.Stderr = os.Environ(), os.Stdout, os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Signal(syscall.SIGTERM)
					_ = command.Wait()
				}
			})
			macOwnershipReady(t, options.EvidenceDirectory)
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			var exitError *exec.ExitError
			if err := command.Wait(); !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
				t.Errorf("public ownership command signal %s: %v", signal.String(), err)
			}
			summary := macOwnershipEvidence(t, options.EvidenceDirectory)
			macRequireOwnedAbsent(t, docker, summary)
			if !summary.FullCleanup || summary.ExitCode != 130 {
				t.Errorf("signal evidence did not prove complete fixture cleanup: %+v", summary)
			}
			if _, err := docker.ContainerInspect(t.Context(), marker.ID, client.ContainerInspectOptions{}); err != nil {
				t.Errorf("signal removed baseline marker: %v", err)
			}
			macCacheMarker(t, docker, marker.ID, "/root/.cache/go-build/mac-runner-marker")
			macCacheMarker(t, docker, marker.ID, "/go/pkg/mod/mac-runner-marker")
		})
	}
}
