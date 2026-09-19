// backup_restore_drill_scratch_test.go boots the drill's throwaway
// FoundationDB through the drill's own boot step and proves its data lands in
// the run's directory under the backup root, not in a Docker volume, that the
// engine can write there, and that the drill's teardown removes the directory.
// The yugabyte counterpart is backup_restore_drill_scratch_yb_test.go. Both
// need a Docker daemon that sees this process's files, since the scratch
// directories are bind-mount sources.

package ops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// scratchDrillTimeout bounds one scratch engine test, which pulls and boots a
// real engine.
const scratchDrillTimeout = 20 * time.Minute

func TestRestoreDrillFDBScratchLivesUnderBackupRoot(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "fdb")
	cfg := &config.Config{
		BackupRoot:           filepath.Join(t.TempDir(), "backups"),
		BackupFDBImage:       image,
		BackupFDBOverlayPath: repoFilePath(t, "fdb-overlay", "fdb.bash"),
		BackupFDBNetwork:     scratchDrillNetwork(ctx, t, cli),
	}
	drill := &restoreDrillCtx{Cfg: cfg, Cli: cli, RunID: "rtscratch-fdb-" + time.Now().UTC().Format("20060102T150405Z")}
	requireDaemonSeesFiles(ctx, t, drill, image)
	t.Cleanup(func() { cleanupRestoreDrill(ctx, drill) })
	name := "tack-rtfdb-" + drill.RunID

	if err := bootScratchFDB(ctx, drill, name, nil); err != nil {
		t.Fatalf("boot the scratch FoundationDB: %v", err)
	}

	runDir := drillScratchRunDir(drill)
	mounts := scratchMounts(ctx, t, cli, name)
	requireScratchBind(t, mounts, "/var/fdb/data", filepath.Join(runDir, "fdb", "data"))
	requireScratchBind(t, mounts, "/var/fdb/logs", filepath.Join(runDir, "fdb", "logs"))
	for target, point := range mounts {
		if point.Type == mount.TypeVolume {
			t.Fatalf("the scratch FoundationDB keeps %s in Docker volume %s; its data must live under the backup root", target, point.Name)
		}
	}
	if !holdsEntries(t, filepath.Join(runDir, "fdb", "data")) {
		t.Fatal("the configured scratch FoundationDB wrote no data under the backup root")
	}

	cleanupRestoreDrill(ctx, drill)
	requireScratchRemoved(t, runDir)
}

// scratchDrillDocker demands a Docker daemon and returns a bounded context and
// a client on it.
func scratchDrillDocker(t *testing.T) (context.Context, *client.Client) {
	t.Helper()
	testenv.RequireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), scratchDrillTimeout)
	t.Cleanup(cancel)
	cli, err := newLocalDockerClient(ctx)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return ctx, cli
}

// scratchDrillNetwork creates an IPv4 bridge for the scratch engines, which
// reach each other by container name the way they do on the stack's network.
func scratchDrillNetwork(ctx context.Context, t *testing.T, cli *client.Client) string {
	t.Helper()
	name := "tack-drill-scratch-" + time.Now().UTC().Format("20060102T150405.000000000")
	enableIPv4, enableIPv6 := true, false
	if _, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge", EnableIPv4: &enableIPv4, EnableIPv6: &enableIPv6,
	}); err != nil {
		t.Fatalf("create network %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.WithoutCancel(ctx), name, client.NetworkRemoveOptions{}) })
	return name
}

// repoFilePath returns the absolute path of a file in the repository, which
// go test runs two levels below.
func repoFilePath(t *testing.T, parts ...string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatalf("resolve %v: %v", parts, err)
	}
	return path
}

// requireDaemonSeesFiles skips the test when the daemon cannot see a file
// this process wrote under the backup root. That is the case when the test
// runs in a container talking to the host's daemon, where a bind-mount source
// names a host path this process cannot read; the CI integration job runs
// these tests on the host.
func requireDaemonSeesFiles(ctx context.Context, t *testing.T, drill *restoreDrillCtx, image string) {
	t.Helper()
	if err := os.MkdirAll(drill.Cfg.BackupRoot, 0o755); err != nil {
		t.Fatalf("mkdir backup root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(drill.Cfg.BackupRoot, "sentinel"), nil, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	res, err := runOneShot(ctx, drill.Cli, nopLogger(), runOneShotOptions{
		Image: image, Network: drill.Cfg.BackupFDBNetwork, Entrypoint: []string{"test"},
		Cmd: []string{"-f", "/probe/sentinel"}, Binds: []string{drill.Cfg.BackupRoot + ":/probe:ro"},
	})
	if err != nil {
		t.Fatalf("probe the daemon's view of %s: %v", drill.Cfg.BackupRoot, err)
	}
	if res.ExitCode != 0 {
		t.Skipf("the Docker daemon cannot see %s, so this process is not on the daemon's host; run on the host", drill.Cfg.BackupRoot)
	}
}

// scratchMounts returns a container's mounts by their path inside it.
func scratchMounts(ctx context.Context, t *testing.T, cli *client.Client, name string) map[string]mountPoint {
	t.Helper()
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", name, err)
	}
	mounts := map[string]mountPoint{}
	for _, point := range inspected.Container.Mounts {
		mounts[point.Destination] = mountPoint{Type: point.Type, Name: point.Name, Source: point.Source}
	}
	return mounts
}

// mountPoint is the part of a container mount the scratch tests check.
type mountPoint struct {
	Type   mount.Type
	Name   string
	Source string
}

// requireScratchBind fails unless target is a bind mount of source.
func requireScratchBind(t *testing.T, mounts map[string]mountPoint, target, source string) {
	t.Helper()
	point, found := mounts[target]
	if !found || point.Type != mount.TypeBind || point.Source != source {
		t.Fatalf("%s must be a bind mount of %s, got %+v (found %t)", target, source, point, found)
	}
}

// holdsEntries reports whether dir lists anything. Only the top level is
// read: the engine writes the entries as root, and this process may not be
// allowed to list the directories it created.
func holdsEntries(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("list %s: %v", dir, err)
	}
	return len(entries) > 0
}

// requireScratchRemoved fails unless the run's scratch directory is gone.
func requireScratchRemoved(t *testing.T, runDir string) {
	t.Helper()
	if _, err := os.Stat(runDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the drill's teardown left %s behind (stat err %v)", runDir, err)
	}
}
