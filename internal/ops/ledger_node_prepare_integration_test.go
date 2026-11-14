// ledger_node_prepare_integration_test.go checks config reads and writes with
// a real Docker container. Rewrites must preserve owner and mode. Missing files
// must report absence. testenv.RequireDocker requires the daemon.

package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestLauncherConfigRewritePreservesContainerFileMetadata(t *testing.T) {
	testenv.RequireDocker(t)
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

	// Match the launcher's uid 1000 ownership. Rewrites must preserve it to
	// permit later launcher writes. Set owner and mode before publishing the
	// staged file to prevent a read of an incomplete config.
	configPath := ledgerLauncherConfigDir + "/" + ledgerLauncherConfigName
	script := "mkdir -p " + ledgerLauncherConfigDir + " && printf '%s' \"$CONF\" > " + configPath + ".staged" +
		" && chown 1000:1000 " + configPath + ".staged" +
		" && chmod 0644 " + configPath + ".staged" +
		" && mv " + configPath + ".staged " + configPath + " && sleep 300"
	created, err := cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{
				Image: image,
				Cmd:   []string{"sh", "-c", script},
				Env:   []string{"CONF=" + savedLauncherConfig},
			},
		},
	)
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

	var saved launcherConfigFile
	var found bool

	for attempt := 0; attempt < 50; attempt++ {
		saved, found, err = readLauncherConfig(ctx, cli, created.ID)
		if err != nil {
			t.Fatalf("read saved config: %v", err)
		}

		if found && len(saved.content) > 0 {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if !found {
		t.Fatal("the saved config the container wrote was not found")
	}

	if saved.header.Uid != 1000 || saved.header.Gid != 1000 {
		t.Fatalf("owner = %d:%d, want the file's own 1000:1000", saved.header.Uid, saved.header.Gid)
	}

	current, err := savedLedgerMasters(context.Background(), saved.content)
	if err != nil {
		t.Fatalf("savedLedgerMasters: %v", err)
	}

	if !equalStringSets(current, []string{"yb1:7100", "yb2:7100", "yugabyte:7100"}) {
		t.Fatalf("saved masters = %v, want the fixture's stale list", current)
	}

	wanted := ledgerMasterList([]string{"yb1", "yb2", "yb3"})
	rewritten, err := rewriteLedgerMasters(ctx, saved.content, wanted)
	if err != nil {
		t.Fatalf("rewriteLedgerMasters: %v", err)
	}

	if err := writeLauncherConfig(ctx, cli, created.ID, saved.header, rewritten); err != nil {
		t.Fatalf("writeLauncherConfig: %v", err)
	}

	after, found, err := readLauncherConfig(ctx, cli, created.ID)
	if err != nil || !found {
		t.Fatalf("read after rewrite: found=%v err=%v", found, err)
	}

	masters, err := savedLedgerMasters(context.Background(), after.content)
	if err != nil {
		t.Fatalf("savedLedgerMasters after rewrite: %v", err)
	}

	if !equalStringSets(masters, wanted) {
		t.Fatalf("masters after rewrite = %v, want %v", masters, wanted)
	}

	if after.header.Uid != 1000 || after.header.Gid != 1000 ||
		after.header.Mode != saved.header.Mode {
		t.Fatalf(
			"owner/mode after rewrite = %d:%d %o, want 1000:1000 %o",
			after.header.Uid,
			after.header.Gid,
			after.header.Mode,
			saved.header.Mode,
		)
	}

	if !strings.Contains(string(after.content), `"join": "yb2"`) {
		t.Fatal("the rewrite must preserve the other keys")
	}

	// A container that has not started the launcher does not have a saved
	// config. The read must report absence rather than an error.
	res, err := containerExec(
		ctx,
		cli,
		created.ID,
		[]string{"rm", ledgerLauncherConfigDir + "/" + ledgerLauncherConfigName},
	)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("remove the file: exit=%d err=%v", res.ExitCode, err)
	}

	_, found, err = readLauncherConfig(ctx, cli, created.ID)
	if err != nil || found {
		t.Fatalf("a missing file must read as absent: found=%v err=%v", found, err)
	}

	_, found, err = readLauncherConfig(ctx, cli, "tack-no-such-container-"+created.ID[:8])
	if err != nil || found {
		t.Fatalf("a missing container must read as absent: found=%v err=%v", found, err)
	}
}
