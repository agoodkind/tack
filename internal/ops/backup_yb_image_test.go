package ops

import (
	"context"
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// assertRefusesEmptyYBImage checks that a refusal identifies the required
// variable as a configuration error instead of an engine failure.
func assertRefusesEmptyYBImage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("an empty TACK_BACKUP_YB_IMAGE must stop the command before it runs a container")
	}

	if !strings.Contains(err.Error(), "TACK_BACKUP_YB_IMAGE") {
		t.Fatalf("error must name TACK_BACKUP_YB_IMAGE, got: %v", err)
	}
}

// TestRunBackupYBPITRInitRequiresImage checks that yb-pitr-init rejects an
// empty engine image before opening a Docker client. The configuration
// omits other required values; attempting the yb-admin container would
// produce a different error.
func TestRunBackupYBPITRInitRequiresImage(t *testing.T) {
	err := RunBackupYBPITRInit(context.Background(), &config.Config{YugabyteDB: "tack"})
	assertRefusesEmptyYBImage(t, err)
}

// TestRunBackupYBSnapshotExportRequiresImage checks that the export rejects
// an empty image after its object-store and password checks and before
// staging files. The test requires BackupRoot to remain empty.
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
		t.Fatalf("no stage directory may be created when the export cannot run, found %v", entries)
	}
}

// TestRestoreDrillYugabyteRequiresImage checks that the ledger restore
// component rejects an empty image before using the object store or Docker.
// The drill uses a nil Docker client; a container call would fail before the
// expected missing-image assertion.
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
