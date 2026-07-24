package ops

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

func TestRunBackupFDBRequiresObjectStore(t *testing.T) {
	backup := &backupCtx{
		Cfg: &config.Config{},
		Log: slog.Default(),
	}

	err := runBackupFDB(context.Background(), backup)
	if err == nil {
		t.Fatal("expected missing object-store endpoint error")
	}
	if !strings.Contains(err.Error(), "TACK_BACKUP_S3_ENDPOINT is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFDBBackupStartArgsAreContinuousWhenFlagIsFalse(t *testing.T) {
	backup := &backupCtx{
		Cfg: &config.Config{
			BackupS3Endpoint:          "http://seaweedfs-s3:8333",
			BackupS3AccessKey:         "key",
			BackupS3SecretKey:         "secret",
			BackupS3Region:            "us-east-1",
			BackupS3BucketMain:        "tack-backups",
			BackupFDBSnapshotInterval: 123,
		},
		Log:   slog.Default(),
		RunID: "run",
	}

	command, binds, extraHosts, err := fdbBackupStartArgs(backup)
	if err != nil {
		t.Fatalf("fdbBackupStartArgs: %v", err)
	}
	joinedCommand := strings.Join(command, " ")
	if strings.Contains(joinedCommand, "file:///") || strings.Contains(joinedCommand, " -w ") {
		t.Fatalf("unexpected local snapshot arguments: %v", command)
	}
	if !strings.Contains(joinedCommand, "blobstore://") ||
		!strings.Contains(joinedCommand, "--snapshot-interval 123 -z") {
		t.Fatalf("missing continuous backup arguments: %v", command)
	}
	if len(binds) != 1 || binds[0] != "/etc/foundationdb:/etc/foundationdb:ro" {
		t.Fatalf("unexpected binds: %v", binds)
	}
	if extraHosts != nil {
		t.Fatalf("unexpected extra hosts: %v", extraHosts)
	}
}
