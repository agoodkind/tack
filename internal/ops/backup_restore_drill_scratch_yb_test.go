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
)

func TestRestoreDrillYugabyteScratchLivesUnderBackupRoot(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	image := composeServiceImage(t, "yugabyte")
	cfg := &config.Config{
		BackupRoot:          filepath.Join(t.TempDir(), "backups"),
		BackupYBImage:       image,
		BackupYBOverlayPath: repoFilePath(t, "yugabyte-overlay", "yugabyted"),
		BackupFDBNetwork:    scratchDrillNetwork(ctx, t, cli),
	}
	runID := "rtscratch-yb-" + time.Now().UTC().Format("20060102T150405Z")
	drill := &restoreDrillCtx{Cfg: cfg, Cli: cli, RunID: runID, YBPass: "drill-" + runID}
	requireDaemonSeesFiles(ctx, t, drill, image)
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
