// The FoundationDB scratch test invokes the drill's boot step and checks
// that the engine writes to a per-run backup directory through bind mounts.
// The test rejects Docker volumes and requires cleanup to remove the directory.
// The YugabyteDB scratch test checks the corresponding engine behavior.
//
// Both use testenv.SharedDir for the backup root because Docker must access
// bind-mount sources at the same paths as the test process.

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

// scratchDrillTimeout limits a test that pulls and boots a real scratch engine.
const scratchDrillTimeout = 20 * time.Minute

func TestRestoreDrillFDBScratchLivesUnderBackupRoot(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "fdb")
	cfg := &config.Config{
		BackupRoot:           filepath.Join(testenv.SharedDir(t), "backups"),
		BackupFDBImage:       image,
		BackupFDBOverlayPath: repoFilePath(t, "fdb-overlay", "fdb.bash"),
		BackupFDBNetwork:     scratchDrillNetwork(ctx, t, cli),
	}
	drill := &restoreDrillCtx{Cfg: cfg, Cli: cli, RunID: "rtscratch-fdb-" + time.Now().UTC().Format("20060102T150405Z")}
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

// scratchDrillDocker requires a Docker daemon and returns its client with a
// time-limited context.
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

// scratchDrillNetwork creates an IPv4 bridge. The scratch engines can resolve
// each other by container name, as they do on the stack network.
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

// repoFilePath returns an absolute repository file path. Tests run from a
// package directory two levels below the repository root.
func repoFilePath(t *testing.T, parts ...string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatalf("resolve %v: %v", parts, err)
	}
	return path
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

// mountPoint records the container mount fields that the scratch tests check.
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

// holdsEntries reports whether dir contains entries. It lists only the top
// level because the engine creates root-owned entries that this process might
// lack permission to inspect recursively.
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
