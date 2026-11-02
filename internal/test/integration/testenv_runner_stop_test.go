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

// TestStoppedRunnerChild is the test process that
// TestRunnerEndRemovesItsEngines starts in a second runner. It starts one engine
// and waits until that runner ends. TestRunnerEndRemovesItsEngines makes every
// assertion. This test skips when the variable that the parent test sets is
// absent.
func TestStoppedRunnerChild(t *testing.T) {
	if os.Getenv(stoppedRunnerChildEnvironment) == "" {
		t.Skip("TestRunnerEndRemovesItsEngines starts this test in its own runner")
	}
	_ = testenv.FoundationDB(t)
	<-t.Context().Done()
}

// TestRunnerEndRemovesItsEngines starts a test process in a second runner from
// this runner's image and ends that runner. One case sends SIGTERM through
// ContainerKill, as `docker compose run` does when it forwards a signal, and one
// case stops the runner. The engine with the second runner's label must be gone
// when that runner stops. This test's own engine and runner must keep running.
func TestRunnerEndRemovesItsEngines(t *testing.T) {
	runner, err := testenv.RunnerContainer(t.Context())
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

	t.Run("forwarded SIGTERM", func(t *testing.T) {
		child := startSecondRunner(t, cli, runner)
		waitForRunningEngine(t, cli, child)
		if _, err := cli.ContainerKill(t.Context(), child, client.ContainerKillOptions{Signal: "TERM"}); err != nil {
			t.Fatalf("send SIGTERM to runner %s: %v", child, err)
		}
		waitForRunnerExit(t, cli, child)
		requireOnlyChildRemoved(t, cli, runner, child, bystander)
	})
	t.Run("docker stop", func(t *testing.T) {
		child := startSecondRunner(t, cli, runner)
		waitForRunningEngine(t, cli, child)
		if _, err := cli.ContainerStop(t.Context(), child, client.ContainerStopOptions{}); err != nil {
			t.Fatalf("stop runner %s: %v", child, err)
		}
		requireOnlyChildRemoved(t, cli, runner, child, bystander)
	})
}

// waitForRunnerExit waits until the container child stops.
func waitForRunnerExit(t *testing.T, cli *client.Client, child string) {
	t.Helper()
	waited := cli.ContainerWait(t.Context(), child, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-waited.Result:
	case err := <-waited.Error:
		t.Fatalf("wait for runner %s: %v", child, err)
	}
}

// requireOnlyChildRemoved requires that containers with child's runner label are
// gone and that the engine bystander and the container runner still run.
func requireOnlyChildRemoved(t *testing.T, cli *client.Client, runner, child, bystander string) {
	t.Helper()
	if names := labeledContainers(t.Context(), t, cli, child); len(names) != 0 {
		t.Errorf("ending runner %s left its containers: %v", child, names)
	}
	for _, name := range []string{bystander, runner} {
		inspected, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{Size: false})
		if err != nil {
			t.Fatalf("inspect container %s of this test: %v", name, err)
		}
		if !inspected.Container.State.Running {
			t.Errorf("container %s of this test stopped when runner %s ended", name, child)
		}
	}
}

// startSecondRunner starts a container from runner's image, mounts, environment,
// and configured stop timeout that runs TestStoppedRunnerChild. It removes that
// container and the containers with its runner label when the test ends.
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
			StopTimeout: source.Config.StopTimeout,
		},
		HostConfig: &container.HostConfig{Binds: source.HostConfig.Binds, Mounts: source.HostConfig.Mounts},
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
	ctx, cancel := context.WithTimeout(t.Context(), stoppedRunnerStartLimit)
	defer cancel()
	ticker := time.NewTicker(stoppedRunnerPollInterval)
	defer ticker.Stop()
	for {
		for _, name := range listRunnerContainers(ctx, t, cli, child, false) {
			if strings.HasPrefix(name, stoppedRunnerEnginePrefix) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runner %s did not start a running engine within %s: %v", child, stoppedRunnerStartLimit, ctx.Err())
		case <-ticker.C:
		}
	}
}

// labeledContainers returns the names of containers in any state with child's
// runner label.
func labeledContainers(ctx context.Context, t *testing.T, cli *client.Client, child string) []string {
	t.Helper()
	return listRunnerContainers(ctx, t, cli, child, true)
}

// listRunnerContainers returns the names of containers with child's runner
// label. It lists only running containers unless all is true.
func listRunnerContainers(ctx context.Context, t *testing.T, cli *client.Client, child string, all bool) []string {
	t.Helper()
	labeled := client.Filters{}.Add("label", stoppedRunnerLabel+"="+child)
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: all, Filters: labeled})
	if err != nil {
		t.Fatalf("list containers of runner %s: %v", child, err)
	}
	names := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		names = append(names, item.Names...)
	}
	return names
}
