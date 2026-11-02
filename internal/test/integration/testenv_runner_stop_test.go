package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/testenv"
)

const (
	stoppedRunnerChildEnvironment = "TACK_TESTENV_STOP_CHILD"
	stoppedRunnerLabel            = "io.goodkind.tack.testenv.runner"
	stoppedRunnerEnginePrefix     = "/tack-testenv-foundationdb-"
	stoppedRunnerStartLimit       = 10 * time.Minute
	stoppedRunnerPollInterval     = 2 * time.Second
)

// TestStoppedRunnerChild starts one engine and waits inside the runner that
// TestRunnerStopRemovesItsEngines starts.
func TestStoppedRunnerChild(t *testing.T) {
	if os.Getenv(stoppedRunnerChildEnvironment) == "" {
		t.Skip("TestRunnerStopRemovesItsEngines starts this test in its own runner")
	}
	_ = testenv.FoundationDB(t)
	<-t.Context().Done()
}

// TestRunnerStopRemovesItsEngines starts a test process in a second runner that
// copies this runner's Compose settings, and stops that runner with its
// configured stop signal. The engine with that runner's label must be gone when
// the stop returns, and this test's own engine must keep running.
func TestRunnerStopRemovesItsEngines(t *testing.T) {
	ctx := t.Context()
	runner, err := testenv.RunnerContainer(ctx)
	if err != nil {
		t.Fatalf("identify the runner container: %v", err)
	}
	if runner == "" {
		t.Skip("a test process on the Docker host has no runner to stop")
	}
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatalf("build a Docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	bystander := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	child := startSecondRunner(t, cli, runner)
	waitForRunningEngine(t, cli, child)

	if _, err := cli.ContainerStop(ctx, child, client.ContainerStopOptions{}); err != nil {
		t.Fatalf("stop runner %s: %v", child, err)
	}

	if names := labeledContainers(ctx, t, cli, child); len(names) != 0 {
		t.Errorf("stopping runner %s left its containers: %v", child, names)
	}
	inspected, err := cli.ContainerInspect(ctx, bystander, client.ContainerInspectOptions{Size: false})
	if err != nil {
		t.Fatalf("inspect this test's engine %s: %v", bystander, err)
	}
	if !inspected.Container.State.Running {
		t.Errorf("this test's engine %s stopped when runner %s stopped", bystander, child)
	}
}

// startSecondRunner starts a container with the Compose settings of runner that
// runs TestStoppedRunnerChild. It removes that container and the containers with
// its runner label when the test ends.
func startSecondRunner(t *testing.T, cli *client.Client, runner string) string {
	t.Helper()
	ctx := t.Context()
	inspected, err := cli.ContainerInspect(ctx, runner, client.ContainerInspectOptions{Size: false})
	if err != nil {
		t.Fatalf("inspect runner %s: %v", runner, err)
	}
	source := inspected.Container
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      source.Image,
			Entrypoint: source.Config.Entrypoint,
			Cmd: []string{
				"test", "-count=1", "-timeout", "15m", "-run", "^TestStoppedRunnerChild$", "./internal/test/integration",
			},
			Env:         append(source.Config.Env, stoppedRunnerChildEnvironment+"=1"),
			Volumes:     source.Config.Volumes,
			WorkingDir:  source.Config.WorkingDir,
			StopSignal:  source.Config.StopSignal,
			StopTimeout: source.Config.StopTimeout,
		},
		HostConfig: &container.HostConfig{
			Binds:  source.HostConfig.Binds,
			Mounts: source.HostConfig.Mounts,
			Init:   source.HostConfig.Init,
		},
	})
	if err != nil {
		t.Fatalf("create second runner: %v", err)
	}
	child := created.ID
	t.Cleanup(func() {
		cleanupContext := context.WithoutCancel(ctx)
		for _, name := range append(labeledContainers(cleanupContext, t, cli, child), child) {
			_, _ = cli.ContainerRemove(cleanupContext, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})
	if _, err := cli.ContainerStart(ctx, child, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start second runner %s: %v", child, err)
	}
	return child
}

// waitForRunningEngine waits until a FoundationDB engine with child's runner
// label runs.
func waitForRunningEngine(t *testing.T, cli *client.Client, child string) {
	t.Helper()
	deadline := time.Now().Add(stoppedRunnerStartLimit)
	for {
		for _, name := range labeledContainers(t.Context(), t, cli, child) {
			if strings.HasPrefix(name, stoppedRunnerEnginePrefix) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner %s did not start an engine within %s", child, stoppedRunnerStartLimit)
		}
		time.Sleep(stoppedRunnerPollInterval)
	}
}

// labeledContainers returns the names of containers with child's runner label.
func labeledContainers(ctx context.Context, t *testing.T, cli *client.Client, child string) []string {
	t.Helper()
	labeled := client.Filters{}.Add("label", stoppedRunnerLabel+"="+child)
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: labeled})
	if err != nil {
		t.Fatalf("list containers of runner %s: %v", child, err)
	}
	names := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		names = append(names, item.Names...)
	}
	return names
}
