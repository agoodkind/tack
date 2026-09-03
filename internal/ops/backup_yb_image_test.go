package ops

import (
	"context"
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// assertRefusesEmptyYBImage checks a refusal names the variable the operator
// has to set, so the failure reads as configuration and not as an engine fault.
func assertRefusesEmptyYBImage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("an empty TACK_BACKUP_YB_IMAGE must stop the command before it runs a container")
	}
	if !strings.Contains(err.Error(), "TACK_BACKUP_YB_IMAGE") {
		t.Fatalf("error must name TACK_BACKUP_YB_IMAGE, got: %v", err)
	}
}

// TestRunBackupYBPITRInitRequiresImage proves yb-pitr-init refuses an empty
// engine image before it opens a Docker client. The config carries nothing
// else, so reaching the yb-admin one-shot would fail on a different error.
func TestRunBackupYBPITRInitRequiresImage(t *testing.T) {
	err := RunBackupYBPITRInit(context.Background(), &config.Config{YugabyteDB: "tack"})
	assertRefusesEmptyYBImage(t, err)
}

// TestRunBackupYBSnapshotExportRequiresImage proves yb-snapshot-export refuses
// an empty engine image after the object-store and password checks it already
// makes and before it stages anything: BackupRoot points into the test's
// temporary directory, which must stay empty.
func TestRunBackupYBSnapshotExportRequiresImage(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		BackupRoot:        root,
		BackupS3Endpoint:  "http://object-store:8333",
		BackupS3AccessKey: "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey: "test-secret", // gitleaks:allow test placeholder
		YugabyteDB:        "tack",
		YugabytePassword:  "test-password", // gitleaks:allow test placeholder
	}

	err := RunBackupYBSnapshotExport(context.Background(), cfg)

	assertRefusesEmptyYBImage(t, err)
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read %s: %v", root, readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("no stage directory may be created when the export cannot run, found %v", entries)
	}
}

// TestRestoreDrillYugabyteRequiresImage proves the drill's ledger leg refuses an
// empty engine image before it touches the object store or Docker. The context
// carries a nil Docker client, so any container call would fail on it rather
// than on the assertion below.
func TestRestoreDrillYugabyteRequiresImage(t *testing.T) {
	drill := &restoreDrillCtx{
		Cfg: &config.Config{
			BackupYBOverlayPath: "/root/tack/yugabyte-overlay/yugabyted",
			BackupYBRocksDBDir:  "/home/yugabyte/var/data/yb-data/tserver/data/rocksdb",
		},
		RunID: "rt-test",
	}

	err := restoreDrillYugabyte(context.Background(), drill)

	assertRefusesEmptyYBImage(t, err)
}
