// dockerctl_exec_test.go proves a container exec ends when its context does.
// It needs a Docker daemon, so it is gated the same way the deploy round trip
// is and skips in the unit suite.

package ops

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// TestContainerExecEndsWhenItsContextDoes is the mechanism under the restore
// watch's probe deadlines. The exec's attach is a hijacked connection the SDK
// never ties to the context, so a command that does not exit held the read
// past any deadline; the drill's probes then hung on the exec rather than on
// the restore. Here a command that sleeps far past the deadline must return
// with the deadline, not with the command.
func TestContainerExecEndsWhenItsContextDoes(t *testing.T) {
	if os.Getenv("DEPLOY_TEST_INTEGRATION") != "1" {
		t.Skip("DEPLOY_TEST_INTEGRATION!=1; skipping daemon-bound integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, err := newLocalDockerClient(ctx)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	const image = "alpine:3"
	if err := ensureImage(ctx, cli, nopLogger(), image); err != nil {
		t.Fatalf("ensure %s: %v", image, err)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Cmd: []string{"sleep", "300"}},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() {
		teardown, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		removeContainerForce(teardown, cli, created.ID)
	})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start container: %v", err)
	}

	const deadline = 2 * time.Second
	probeCtx, cancelProbe := context.WithTimeout(ctx, deadline)
	defer cancelProbe()
	started := time.Now()
	_, err = containerExec(probeCtx, cli, created.ID, []string{"sleep", "120"})
	took := time.Since(started)

	if err == nil {
		t.Fatal("an exec that outlives its context must return an error")
	}
	if took > 10*deadline {
		t.Fatalf("the exec held the read for %s past a %s deadline", took, deadline)
	}
}
