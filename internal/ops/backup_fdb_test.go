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

func TestFDBBackupActiveFromStatusAcceptsConfiguredDestination(t *testing.T) {
	cfg := &config.Config{
		BackupS3Endpoint:   "http://[3d06:bad:b01:204::410]:8333",
		BackupS3BucketMain: "tack-backups",
	}
	status := "The backup on tag `default' is in progress to a blobstore URL.\n" +
		"BackupURL: blobstore://status-access:status-secret@fdb-blobstore-host:8333/run" + // gitleaks:allow test placeholder
		"?bucket=tack-backups&region=us-east-1&secure_connection=0\n"

	active, err := fdbBackupActiveFromStatus(cfg, status)
	if err != nil {
		t.Fatalf("fdbBackupActiveFromStatus: %v", err)
	}
	if !active {
		t.Fatal("expected configured active backup")
	}
}

func TestFDBBackupActiveFromStatusRejectsDifferentDestination(t *testing.T) {
	cfg := &config.Config{
		BackupS3Endpoint:   "http://seaweedfs-s3:8333",
		BackupS3BucketMain: "tack-backups",
	}
	tests := []struct {
		name      string
		backupURL string
	}{
		{
			name: "bucket",
			backupURL: "blobstore://status-access:status-secret@seaweedfs-s3:8333/run" + // gitleaks:allow test placeholder
				"?bucket=other-backups&region=us-east-1&secure_connection=0",
		},
		{
			name: "host",
			backupURL: "blobstore://status-access:status-secret@other-store:8333/run" + // gitleaks:allow test placeholder
				"?bucket=tack-backups&region=us-east-1&secure_connection=0",
		},
		{
			name: "port",
			backupURL: "blobstore://status-access:status-secret@seaweedfs-s3:9333/run" + // gitleaks:allow test placeholder
				"?bucket=tack-backups&region=us-east-1&secure_connection=0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := "The backup on tag `default' is in progress to a blobstore URL.\n" +
				"BackupURL: " + test.backupURL + "\n"

			active, err := fdbBackupActiveFromStatus(cfg, status)
			if err == nil {
				t.Fatal("expected destination mismatch error")
			}
			if active {
				t.Fatal("mismatched backup must not be accepted as active")
			}
			if !strings.Contains(err.Error(), "different destination") {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), test.backupURL) ||
				strings.Contains(err.Error(), "status-access") ||
				strings.Contains(err.Error(), "status-secret") {
				t.Fatalf("error leaked BackupURL credentials: %v", err)
			}
		})
	}
}

func TestRedactSecretMasksS3Credentials(t *testing.T) {
	cfg := &config.Config{
		BackupS3AccessKey: "access-key", // gitleaks:allow test placeholder
		BackupS3SecretKey: "secret-key", // gitleaks:allow test placeholder
	}
	input := "blobstore://access-key:secret-key@seaweedfs-s3:8333/run" // gitleaks:allow test placeholder

	got := redactSecret(cfg, input)

	if strings.Contains(got, cfg.BackupS3AccessKey) {
		t.Fatalf("redacted text leaked access key: %q", got)
	}
	if strings.Contains(got, cfg.BackupS3SecretKey) {
		t.Fatalf("redacted text leaked secret key: %q", got)
	}
	want := "blobstore://***REDACTED***:***REDACTED***@seaweedfs-s3:8333/run"
	if got != want {
		t.Fatalf("unexpected redacted text: got %q want %q", got, want)
	}
}
