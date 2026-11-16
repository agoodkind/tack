package ops

import (
	"context"
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// assertRefusesEmptyYBImage checks that the error identifies the missing image
// variable instead of reporting an engine failure.
func assertRefusesEmptyYBImage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("an empty TACK_BACKUP_YB_IMAGE must stop the command before it runs a container")
	}

	if !strings.Contains(err.Error(), "TACK_BACKUP_YB_IMAGE") {
		t.Fatalf("error must identify TACK_BACKUP_YB_IMAGE, got: %v", err)
	}
}

// TestRunBackupYBPITRInitRequiresImage checks rejection before Docker access.
// The configuration omits other inputs that a container call would require.
func TestRunBackupYBPITRInitRequiresImage(t *testing.T) {
	err := RunBackupYBPITRInit(context.Background(), &config.Config{YugabyteDB: "tack"})
	assertRefusesEmptyYBImage(t, err)
}

// TestRunBackupYBSnapshotExportRequiresImage checks rejection after store and
// password validation but before staging creates any files.
func TestRunBackupYBSnapshotExportRequiresImage(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		BackupRoot:        root,
		BackupS3Endpoint:  "http://object-store:8333",
		BackupS3AccessKey: "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey: "test-secret", // gitleaks:allow test placeholder
		YugabyteDB:        "tack",
		MigratorPassword:  "test-password", // gitleaks:allow test placeholder
	}

	err := RunBackupYBSnapshotExport(context.Background(), cfg)

	assertRefusesEmptyYBImage(t, err)
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read %s: %v", root, readErr)
	}

	if len(entries) != 0 {
		t.Fatalf("export rejection must not create a staging directory, found %v", entries)
	}
}

// TestRestoreDrillYugabyteRequiresImage checks rejection before object-store
// or Docker access. The context does not supply a Docker client.
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
