// The YugabyteDB scratch test invokes the drill's start and readiness checks.
// It requires the engine to write its base directory under the run's backup
// directory and requires cleanup to remove it. The run ID uses the maximum
// Linux process ID to exercise the longest production container-name form.
// The test logs startup duration to check the address-validation regression.
// The companion scratch test supplies the shared helpers.

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

// linuxPIDMax supplies the maximum process ID for the 64-bit Linux run-ID
// fixture. It exercises the longest process-ID suffix.
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
	// The run ID uses the drill's timestamp format and maximum Linux process ID.
	// Its container name has the longest production form. Advertising that name
	// previously made yugabyted's address check take 39 seconds natively and exceed
	// the drill's stall window under emulation, as recorded in TACK-527.
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

	watch := newYBScratchWatch(
		drill,
		name,
		"",
		[]string{"PGPASSWORD=" + drill.YBPass},
		ysqlshArgs(ybScratchHost(name), database, "select 1"),
	)
	took, err := awaitYBScratch(
		ctx,
		"scratch yugabyted start",
		watch,
		ybScratchStallWindow,
		ybScratchPollInterval,
		ybScratchProbeTimeout,
	)
	if err != nil {
		t.Fatalf(
			"the scratch yugabyted never answered from a base directory under the backup root: %v",
			err,
		)
	}

	t.Logf("the scratch yugabyted for container %s answered after %s", name, took)

	runDir := drillScratchRunDir(drill)
	baseDir := filepath.Join(runDir, "yb")
	requireScratchBind(t, scratchMounts(ctx, t, cli, name), "/home/yugabyte/var", baseDir)
	if !holdsEntries(t, baseDir) {
		t.Fatal(
			"the ready scratch yugabyted wrote nothing into its base directory under the backup root",
		)
	}

	cleanupRestoreDrill(ctx, drill)
	requireScratchRemoved(t, runDir)
}
