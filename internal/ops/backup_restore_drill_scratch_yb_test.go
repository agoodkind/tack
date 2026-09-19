// backup_restore_drill_scratch_yb_test.go boots the drill's throwaway
// yugabyted through the drill's own start and readiness steps and proves its
// base directory is the run's directory under the backup root, that the
// engine can write there, and that the drill's teardown removes it. The
// helpers are in backup_restore_drill_scratch_test.go.

package ops

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

func TestRestoreDrillYugabyteScratchLivesUnderBackupRoot(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "yugabyte")
	cfg := &config.Config{
		BackupRoot:          filepath.Join(testenv.SharedDir(t), "backups"),
		BackupYBImage:       image,
		BackupYBOverlayPath: repoFilePath(t, "yugabyte-overlay", "yugabyted"),
		BackupFDBNetwork:    scratchDrillNetwork(ctx, t, cli),
	}
	// The run ID is short because the scratch container's name is also its
	// advertise address, and yugabyted checks that address against a DNS
	// regex whose cost doubles with each character when the name does not end
	// in two letters. A 39-character name took that regex 55s in native
	// Python and held the emulated amd64 launcher on an arm64 host past the
	// drill's stall window (TACK-522).
	runID := "rtyb-" + time.Now().UTC().Format("150405")
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
		ysqlshArgs(name, database, "select 1"))
	if _, err := awaitYBScratch(ctx, "scratch yugabyted start", watch,
		ybScratchStallWindow, ybScratchPollInterval, ybScratchProbeTimeout); err != nil {
		t.Fatalf("the scratch yugabyted never answered from a base directory under the backup root: %v", err)
	}

	runDir := drillScratchRunDir(drill)
	baseDir := filepath.Join(runDir, "yb")
	requireScratchBind(t, scratchMounts(ctx, t, cli, name), "/home/yugabyte/var", baseDir)
	if !holdsEntries(t, baseDir) {
		t.Fatal("the ready scratch yugabyted wrote nothing into its base directory under the backup root")
	}

	cleanupRestoreDrill(ctx, drill)
	requireScratchRemoved(t, runDir)
}
