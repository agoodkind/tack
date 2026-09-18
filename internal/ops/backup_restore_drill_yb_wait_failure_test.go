// backup_restore_drill_yb_wait_failure_test.go runs the failure check against
// a real container: no log yet reads as zero restarts, restart lines are
// counted, and a container that is gone is a read failure. It needs a Docker
// daemon, so it is gated the same way the exec deadline test is and skips in
// the unit suite.

package ops

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestYBScratchFailureProbeReadsAMissingLogAsZeroRestarts(t *testing.T) {
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
	probe := newYBScratchFailureProbe(&restoreDrillCtx{Cli: cli}, created.ID, "")

	failure, err := probe(ctx)
	if err != nil || failure != "" {
		t.Fatalf("before the log exists: failure %q, err %v; want no failure and no error", failure, err)
	}

	write := "mkdir -p /home/yugabyte/var/logs && printf '%s\\n%s\\n%s\\n' " +
		"'| 3.5s | master died unexpectedly. Restarting...' " +
		"'| 4.0s | Webserver died unexpectedly. Restarting...' " +
		"'| 9.1s | tserver died unexpectedly. Restarting...' > " + ybScratchLogPath
	if res, err := containerExec(ctx, cli, created.ID, []string{"sh", "-c", write}); err != nil || res.ExitCode != 0 {
		t.Fatalf("write the log: exit %d, err %v, stderr %q", res.ExitCode, err, res.Stderr)
	}
	failure, err = probe(ctx)
	if err != nil {
		t.Fatalf("after two restart lines: %v", err)
	}
	if !strings.Contains(failure, "2 time(s)") {
		t.Fatalf("failure = %q, want the two engine restarts counted and the web server's ignored", failure)
	}

	removeContainerForce(ctx, cli, created.ID)
	if _, err := probe(ctx); err == nil {
		t.Fatal("a container that is gone must read as a failed check, not as zero restarts")
	}
}
