// backup_restore_drill_orphans_test.go leaves behind what a restore drill
// killed before its teardown leaves, starts a drill beside a drill that is
// still running, and proves the new drill's claim removes the killed run's
// scratch engine, scratch directory, and staging directory while the running
// drill keeps its own. Its backup root is a testenv.SharedDir, since the
// scratch directory is removed through a bind mount.

package ops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

func TestClaimDrillRunSweepsKilledRunAndKeepsLiveRun(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "fdb")
	cfg := &config.Config{
		BackupRoot:       filepath.Join(testenv.SharedDir(t), "backups"),
		BackupYBImage:    image,
		BackupFDBNetwork: scratchDrillNetwork(ctx, t, cli),
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	newDrill := func(pid string) *restoreDrillCtx {
		return &restoreDrillCtx{Cfg: cfg, Cli: cli, RunID: "rt" + stamp + "-" + pid}
	}
	live, killed, current := newDrill("101"), newDrill("102"), newDrill("103")

	if err := claimDrillRun(ctx, live); err != nil {
		t.Fatalf("claim the live run: %v", err)
	}
	t.Cleanup(func() { cleanupRestoreDrill(ctx, live) })
	liveEngine := startSleepingEngine(ctx, t, cli, image, "tack-rtyb-"+live.RunID, drillScratchRunDir(live))

	if err := claimDrillRun(ctx, killed); err != nil {
		t.Fatalf("claim the killed run: %v", err)
	}
	killedEngine := startSleepingEngine(ctx, t, cli, image, "tack-rtfdb-"+killed.RunID, drillScratchRunDir(killed))
	killedStage := filepath.Join(cfg.BackupRoot, "restore-drill-yb-"+killed.RunID)
	if err := os.MkdirAll(filepath.Join(killedStage, "staged"), 0o755); err != nil {
		t.Fatalf("mkdir the killed run's staging directory: %v", err)
	}
	// A killed process runs no teardown; the kernel only drops its lock.
	releaseDrillRun(killed)

	if err := claimDrillRun(ctx, current); err != nil {
		t.Fatalf("claim the current run: %v", err)
	}
	t.Cleanup(func() { cleanupRestoreDrill(ctx, current) })

	if _, err := cli.ContainerInspect(ctx, killedEngine, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("the killed run's engine %s is still there after the sweep (inspect err %v)", killedEngine, err)
	}
	for _, dir := range []string{drillScratchRunDir(killed), killedStage} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the sweep left the killed run's %s (stat err %v)", dir, err)
		}
	}
	inspected, err := cli.ContainerInspect(ctx, liveEngine, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.State == nil || !inspected.Container.State.Running {
		t.Errorf("the sweep stopped or removed the live run's engine %s (inspect err %v)", liveEngine, err)
	}
	if _, err := os.Stat(drillScratchRunDir(live)); err != nil {
		t.Errorf("the sweep removed the live run's scratch directory: %v", err)
	}
}

// startSleepingEngine starts a container named like a scratch engine that
// binds scratchDir the way one does, and removes it when the test ends.
func startSleepingEngine(ctx context.Context, t *testing.T, cli *client.Client, image, name, scratchDir string) string {
	t.Helper()
	if err := ensureImage(ctx, cli, nopLogger(), image); err != nil {
		t.Fatalf("ensure image %s: %v", image, err)
	}
	t.Cleanup(func() { removeContainerForce(context.WithoutCancel(ctx), cli, name) })
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, Entrypoint: []string{"sleep"}, Cmd: []string{"3600"}},
		HostConfig: &container.HostConfig{Binds: []string{scratchDir + ":/scratch"}},
		Name:       name,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return name
}
