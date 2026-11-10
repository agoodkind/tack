// backup_restore_drill_scratch_yb_test.go boots the drill's throwaway
// yugabyted through the drill's own start and readiness steps and proves its
// base directory is the run's directory under the backup root, that the
// engine can write there, and that the drill's teardown removes it. It boots
// under the longest run id a production drill can have, the one yugabyted's
// advertise-address check would be slowest on, and logs how long the start
// took. The helpers are in
// backup_restore_drill_scratch_test.go.

package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// linuxPIDMax is the largest pid a 64-bit Linux kernel can hand out, and so
// the longest pid a drill's run id can end in.
const linuxPIDMax = 4194304

func TestRestoreDrillYugabyteScratchLivesUnderBackupRoot(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "yugabyte")
	cfg := &config.Config{
		BackupRoot:          filepath.Join(testenv.SharedDir(t), "backups"),
		BackupYBImage:       image,
		BackupYBOverlayPath: repoFilePath(t, "yugabyte-overlay", "yugabyted"),
		BackupFDBNetwork:    scratchDrillNetwork(ctx, t, cli),
	}
	// The run ID has the drill's own shape with the largest pid Linux hands
	// out, so the container name is as long as a production run's can be. A
	// name that long is what yugabyted's advertise-address check backtracks
	// over for 39s natively and past the drill's stall window under emulation
	// when the drill advertises the container name itself (TACK-527).
	runID := fmt.Sprintf("rt%s-%d", time.Now().UTC().Format("20060102T150405Z"), linuxPIDMax)
	if !drillRunIDPattern.MatchString(runID) {
		t.Fatalf("run id %q does not have the shape the drill gives its runs", runID)
	}
	drill := &restoreDrillCtx{Cfg: cfg, Cli: cli, RunID: runID, YBPass: "drill-" + runID}
	t.Cleanup(func() { cleanupRestoreDrill(ctx, drill) })
	stageDir := filepath.Join(cfg.BackupRoot, "restore-drill-yb-"+runID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	const database = "tack"
	name := "tack-rtyb-" + runID

	if err := startScratchYugabyte(ctx, drill, name, database, stageDir); err != nil {
		t.Fatalf("start the scratch yugabyted: %v", err)
	}
	watch := newYBScratchWatch(drill, name, "", []string{"PGPASSWORD=" + drill.YBPass},
		ysqlshArgs(ybScratchHost(name), database, "select 1"))
	took, err := awaitYBScratch(ctx, "scratch yugabyted start", watch,
		ybScratchStallWindow, ybScratchPollInterval, ybScratchProbeTimeout)
	if err != nil {
		t.Fatalf("the scratch yugabyted never answered from a base directory under the backup root: %v", err)
	}
	t.Logf("the scratch yugabyted for container %s answered after %s", name, took)

	runDir := drillScratchRunDir(drill)
	baseDir := filepath.Join(runDir, "yb")
	requireScratchBind(t, scratchMounts(ctx, t, cli, name), "/home/yugabyte/var", baseDir)
	if !holdsEntries(t, baseDir) {
		t.Fatal("the ready scratch yugabyted wrote nothing into its base directory under the backup root")
	}

	cleanupRestoreDrill(ctx, drill)
	requireScratchRemoved(t, runDir)
}
