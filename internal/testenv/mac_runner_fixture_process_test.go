//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

// macRequireRunnerActive fails the test if result.json exists in directory.
func macRequireRunnerActive(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(directory, "result.json")); err == nil {
		t.Fatalf("the runner ended before the fixture reported readiness; read the result in %s", directory)
	}
}

func macOwnershipReady(t *testing.T, directory string) string {
	t.Helper()
	bounded, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			contents, err := os.ReadFile(filepath.Join(directory, "runtime.log"))
			if err != nil || !strings.Contains(string(contents), "MAC_RUNNER_SIGNAL_READY") {
				macRequireRunnerActive(t, directory)
				continue
			}
			identity, err := os.ReadFile(filepath.Join(directory, "outer-id.txt"))
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(identity))
		case <-bounded.Done():
			t.Fatalf("ownership fixture did not become ready: %v", bounded.Err())
		}
	}
}

func macIndependentOwner(t *testing.T, docker *client.Client, options testenv.MacTestOptions, project string) string {
	t.Helper()
	created, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Name: project + "-independent", Config: &container.Config{
			Image: options.Image, Entrypoint: []string{"/usr/local/go/bin/go"},
			Cmd:        []string{"test", "-tags=fdb,macrunnerfixture,runtimeaudit", "-run=^TestMacOwnershipIndependent$", "-v", "-timeout=10m", "./internal/test/integration/..."},
			WorkingDir: options.Root, Labels: map[string]string{"tack.mac-runner-fixture": project},
		}, HostConfig: &container.HostConfig{Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: options.Root, Target: options.Root},
			{Type: mount.TypeBind, Source: "/var/run/docker.sock.raw", Target: "/var/run/docker.sock"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-cache", Target: "/root/.cache/go-build"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-mod", Target: "/go/pkg/mod"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var engine string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
		if engine == "" {
			return
		}
		inspection, err := docker.ContainerInspect(ctx, engine, client.ContainerInspectOptions{})
		if err != nil || inspection.Container.Config.Labels["io.goodkind.tack.testenv.runner"] != created.ID {
			t.Errorf("refuse independent engine cleanup %s: %v", engine, err)
			return
		}
		if _, err := docker.ContainerRemove(ctx, inspection.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
	})
	if _, err := docker.ContainerStart(t.Context(), created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for engine == "" {
		select {
		case <-ticker.C:
			logs, err := docker.ContainerLogs(bounded, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
			if err != nil {
				t.Fatal(err)
			}
			contents, readErr := io.ReadAll(logs)
			_ = logs.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, line := range strings.Split(string(contents), "\n") {
				_, name, found := strings.Cut(line, "MAC_INDEPENDENT_ENGINE=")
				if found {
					engine = strings.TrimSpace(name)
				}
			}
		case <-bounded.Done():
			t.Fatalf("independent real process did not start its engine: %v", bounded.Err())
		}
	}
	inspection, err := docker.ContainerInspect(t.Context(), engine, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return inspection.Container.ID
}

// TestMacRunnerDockerFixtureKilledProcess runs testenv.RunMacTests with a
// fixture test binary. The binary creates its engines, cluster, proxy,
// network, and anonymous volume. It then sends SIGKILL to itself without
// running cleanup. A verified native Mac runner does not start the run
// watcher. The test requires exit status 1. It requires the outer runner to
// remove every recorded resource and report complete cleanup.
func TestMacRunnerDockerFixtureKilledProcess(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "Killed")
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 1 {
		t.Errorf("killed test binary: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if !summary.Attribution || !summary.Cleanup || !summary.FullCleanup {
		t.Errorf("killed test binary did not finish cleanup: %+v", summary)
	}
}

// TestMacRunnerDockerFixtureTerminatedProcess runs testenv.RunMacTests with a
// fixture test binary. The binary creates its resources, then sends SIGTERM
// to itself. Its releaseOnSignal handler removes its engines. The test requires
// exit status 1, engine logs, and complete attribution. It also requires the
// outer runner to report complete cleanup.
func TestMacRunnerDockerFixtureTerminatedProcess(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "Terminated")
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 1 {
		t.Errorf("terminated test binary: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if !summary.Attribution || !summary.Cleanup || !summary.FullCleanup {
		t.Errorf("terminated test binary did not finish cleanup: %+v", summary)
	}
}

func TestMacRunnerDockerFixtureOtherProcess(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "Release")
	independent := macIndependentOwner(t, docker, options, project)
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 0 {
		t.Errorf("fixture release: result=%+v error=%v", result, err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRequireOwnedAbsent(t, docker, summary)
	if !summary.FullCleanup {
		t.Errorf("fixture release did not finish cleanup: %+v", summary)
	}
	inspection, err := docker.ContainerInspect(t.Context(), independent, client.ContainerInspectOptions{})
	if err != nil || inspection.Container.State == nil || !inspection.Container.State.Running {
		t.Errorf("independent active engine did not survive native release: %s: %v", independent, err)
	}
	if _, found := summary.Fixtures.Containers[independent]; found {
		t.Error("native invocation attributed another process's engine")
	}
	if errdefs.IsNotFound(err) {
		t.FailNow()
	}
}
