//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestMacRunnerDockerOrdinaryOwnerRegression(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "All")
	contents, err := os.ReadFile("release_test.go")
	if err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "internal/test/integration/owner_test.go", strings.Replace(string(contents), "package testenv_test", "package integration", 1))
	compose, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "docker-compose.yml", string(compose))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: project + "-ordinary-regression", Config: &container.Config{
			Image: options.Image, Entrypoint: []string{"/usr/local/go/bin/go"},
			Cmd: []string{"test", "-tags=fdb,integration,macrunnerfixture,runtimeaudit", "-run=^TestReleaseKeepsEnginesOfRunningProcesses$", "-count=1", "-timeout=4m", "-v", "./internal/test/integration/..."}, WorkingDir: options.Root, Labels: map[string]string{"tack.mac-runner-fixture": project},
		},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: options.Root, Target: options.Root},
			{Type: mount.TypeBind, Source: "/var/run/docker.sock.raw", Target: "/var/run/docker.sock"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-cache", Target: "/root/.cache/go-build"},
			{Type: mount.TypeVolume, Source: project + "_tack-test-go-mod", Target: "/go/pkg/mod"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case response := <-wait.Result:
		if response.StatusCode != 0 {
			t.Errorf("ordinary owner regression exit %d", response.StatusCode)
		}
	case err := <-wait.Error:
		t.Error(err)
	}
	logs, err := docker.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	var output strings.Builder
	_, err = stdcopy.StdCopy(&output, &output, logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual existing ordinary owner regression output: %s", output.String())
}
