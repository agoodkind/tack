package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/testenv"
)

const (
	stoppedRunnerChildEnvironment = "TACK_TESTENV_STOP_CHILD"
	stoppedRunnerLabel            = "io.goodkind.tack.testenv.runner"
	stoppedRunnerStartLimit       = 10 * time.Minute
	stoppedRunnerPollInterval     = 2 * time.Second
	stoppedRunnerChildTimeout     = "15m"
	stoppedRunnerPanicTimeout     = "2m"
	stoppedRunnerFailedStatus     = int64(1)
	stoppedRunnerGoneStatus       = int64(-1)
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

// TestRunnerEndRemovesItsEngines starts a test process in a second runner
// from this runner's image. It ends that runner with SIGTERM through
// ContainerKill, SIGINT through ContainerKill, docker stop, and a go test
// timeout panic. docker compose run forwards signals as ContainerKill
// sends them.
//
// The test checks that Docker has removed the containers with the second
// runner's label when that runner stops. It also checks that this test's
// engine and runner remain running. The timeout case requires exit
// status 1.
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

	forward := func(signal string) func(*testing.T, string) {
		return func(t *testing.T, child string) {
			t.Helper()
			if _, err := cli.ContainerKill(t.Context(), child, client.ContainerKillOptions{Signal: signal}); err != nil {
				t.Fatalf("send SIG%s to runner %s: %v", signal, child, err)
			}
			waitForRunnerExit(t, cli, child)
		}
	}
	cases := []struct {
		name    string
		timeout string
		end     func(*testing.T, string)
	}{
		{name: "forwarded SIGTERM", timeout: stoppedRunnerChildTimeout, end: forward("TERM")},
		{name: "forwarded SIGINT", timeout: stoppedRunnerChildTimeout, end: forward("INT")},
		{name: "docker stop", timeout: stoppedRunnerChildTimeout, end: func(t *testing.T, child string) {
			t.Helper()
			if _, err := cli.ContainerStop(t.Context(), child, client.ContainerStopOptions{}); err != nil {
				t.Fatalf("stop runner %s: %v", child, err)
			}
		}},
		{name: "go test timeout", timeout: stoppedRunnerPanicTimeout, end: func(t *testing.T, child string) {
			t.Helper()
			if status := waitForRunnerExit(t, cli, child); status != stoppedRunnerFailedStatus {
				t.Errorf("runner %s exited with status %d after its test timed out, want %d", child, status, stoppedRunnerFailedStatus)
			}
		}},
	}
	for _, runnerEnd := range cases {
		t.Run(runnerEnd.name, func(t *testing.T) {
			child := startSecondRunner(t, cli, runner, runnerEnd.timeout)
			waitForRunningEngine(t, cli, child)
			runnerEnd.end(t, child)
			requireOnlyChildRemoved(t, cli, runner, child, bystander)
		})
	}
}

// waitForRunnerExit waits until the child container stops and returns its
// exit status. It returns stoppedRunnerGoneStatus when Docker no longer
// has the container.
func waitForRunnerExit(t *testing.T, cli *client.Client, child string) int64 {
	t.Helper()
	waited := cli.ContainerWait(t.Context(), child, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-waited.Result:
		return result.StatusCode
	case err := <-waited.Error:
		if !cerrdefs.IsNotFound(err) {
			t.Fatalf("wait for runner %s: %v", child, err)
		}
		return stoppedRunnerGoneStatus
	}
}

// requireOnlyChildRemoved requires that containers with child's runner label are
// gone and that the engine bystander and the container runner still run.
func requireOnlyChildRemoved(t *testing.T, cli *client.Client, runner, child, bystander string) {
	t.Helper()
	if names := listRunnerContainers(t.Context(), t, cli, child, true); len(names) != 0 {
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

// startSecondRunner starts a container from runner's image, environment,
// and configured stop timeout. The container runs TestStoppedRunnerChild
// with the given go test timeout. It shares runner's mounts, which prevents
// Docker from creating an anonymous volume. Docker removes the container
// when it exits. Test cleanup removes the container and containers with
// its runner label and preserves every volume.
func startSecondRunner(t *testing.T, cli *client.Client, runner, timeout string) string {
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
				"test", "-count=1", "-timeout", timeout, "-run", "^TestStoppedRunnerChild$", "./internal/test/integration",
			},
			Env:         append(source.Config.Env, stoppedRunnerChildEnvironment+"=1"),
			WorkingDir:  source.Config.WorkingDir,
			StopTimeout: source.Config.StopTimeout,
		},
		HostConfig: &container.HostConfig{VolumesFrom: []string{runner}, AutoRemove: true},
	})
	if err != nil {
		t.Fatalf("create second runner: %v", err)
	}
	child := created.ID
	t.Cleanup(func() {
		cleanupContext := context.WithoutCancel(ctx)
		for _, name := range append(listRunnerContainers(cleanupContext, t, cli, child, true), child) {
			_, _ = cli.ContainerRemove(cleanupContext, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
		}
	})
	if _, err := cli.ContainerStart(ctx, child, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start second runner %s: %v", child, err)
	}
	return child
}

// waitForRunningEngine waits until a container with child's runner label
// runs.
func waitForRunningEngine(t *testing.T, cli *client.Client, child string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), stoppedRunnerStartLimit)
	defer cancel()
	ticker := time.NewTicker(stoppedRunnerPollInterval)
	defer ticker.Stop()
	for {
		if len(listRunnerContainers(ctx, t, cli, child, false)) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runner %s did not start a running engine within %s: %v", child, stoppedRunnerStartLimit, ctx.Err())
		case <-ticker.C:
		}
	}
}
