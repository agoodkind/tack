package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runBackupFDB backs up FoundationDB through `fdbbackup`. It does not tar the
// data volume, because the FDB image declares `VOLUME /var/fdb/data` and an
// anonymous volume shadows a bind, so a volume tar is silently empty.
//
// There are two modes. In continuous mode it ensures a streaming session to the
// object store is running and returns; the long-lived backup_agent that drains
// snapshots is the `fdb-backup-agent` compose service, not a container started
// here. In one-shot mode it starts a short-lived backup_agent sidecar, runs a
// blocking `fdbbackup start -w` to a local snapshot directory, asserts
// `Restorable: true`, tars the resolved `backup-*` subdirectory, and removes the
// sidecar.
func runBackupFDB(ctx context.Context, b *backupCtx) error {
	if b.Cfg.BackupFDBContinuous {
		return ensureFDBContinuousSession(ctx, b)
	}

	sidecarName := b.Cfg.BackupFDBSidecar
	// Pre-clean any sidecar from a prior aborted run; the helper is
	// no-op when nothing matches.
	removeContainerForce(ctx, b.Cli, sidecarName)

	// Use a derived background context for teardown so signal cancellation
	// of ctx still lets us clean up the sidecar.
	defer func() {
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		removeContainerForce(teardown, b.Cli, sidecarName)
	}()

	err := startFDBSidecar(ctx, b, sidecarName)
	if err != nil {
		return err
	}

	timeoutSec := b.Cfg.BackupFDBTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}
	startCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	err = runFDBBackupStart(startCtx, b)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.start_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("fdbbackup start: %w", err)
	}

	backupURL, subdir, err := resolveFDBBackupSubdir(ctx, b.Log, b.SnapshotDir, b.RunID)
	if err != nil {
		return err
	}
	b.Log.InfoContext(ctx, "backup.fdb.subdir_resolved",
		slog.String("backup_url", backupURL),
		slog.String("subdir", subdir),
	)

	describeOut, err := runFDBBackupDescribe(ctx, b, backupURL)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.describe_failed",
			slog.String("backup_url", backupURL),
			slog.String("output", describeOut),
			slog.Any("err", err),
		)
		return fmt.Errorf("fdbbackup describe: %w (output: %s)", err, describeOut)
	}
	if !strings.Contains(describeOut, "Restorable: true") {
		notRestorableErr := fmt.Errorf("fdbbackup describe did not report Restorable: true")
		b.Log.ErrorContext(ctx, "backup.fdb.not_restorable",
			slog.String("backup_url", backupURL),
			slog.String("describe", describeOut),
			slog.Any("err", notRestorableErr),
		)
		return notRestorableErr
	}

	// Persist the describe output alongside the backup so verify can
	// inspect what fdbbackup saw at snapshot time.
	describePath := filepath.Join(b.DestDir, "fdb", "describe.txt")
	err = os.MkdirAll(filepath.Dir(describePath), 0o750)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.mkdir_failed",
			slog.String("dir", filepath.Dir(describePath)),
			slog.Any("err", err),
		)
		return fmt.Errorf("mkdir fdb dir: %w", err)
	}
	err = os.WriteFile(describePath, []byte(describeOut), 0o600)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.describe_write_failed",
			slog.String("path", describePath),
			slog.Any("err", err),
		)
		return fmt.Errorf("write describe.txt: %w", err)
	}

	tarPath := filepath.Join(b.DestDir, "fdb-snapshot.tar.gz")
	err = tarFDBBackupSubdir(ctx, b, subdir, tarPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.tar_failed",
			slog.String("subdir", subdir),
			slog.String("tar_path", tarPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("tar fdb backup subdir: %w", err)
	}
	b.Log.InfoContext(ctx, "backup.fdb.complete", slog.String("artifact", tarPath))
	return nil
}

// sidecarBlobstoreExtraHosts returns the Docker ExtraHosts the backup_agent
// sidecar needs to resolve the blobstore host in continuous mode. The sidecar
// streams the continuous writes to the object store, so it must resolve the
// synthetic blobstore hostname the same way the fdbbackup one-shot does. It
// returns nil for the one-shot file:// path (BackupFDBContinuous false) and for
// plain-hostname endpoints, leaving the sidecar's HostConfig unchanged.
func sidecarBlobstoreExtraHosts(ctx context.Context, b *backupCtx) ([]string, error) {
	if !b.Cfg.BackupFDBContinuous {
		return nil, nil
	}
	extraHosts, err := blobstoreExtraHosts(b.Cfg.BackupS3Endpoint)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.blobstore_extra_hosts_failed",
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("build blobstore extra hosts: %w", err)
	}
	return extraHosts, nil
}

// runFDBBackupStart fires `fdbbackup start` from a one-shot client container
// that joins the same docker network as the live cluster. The destination and
// flags depend on cfg.BackupFDBContinuous:
//
//   - one-shot (default): `-w -d file:///snapshot/<run-id>`, which blocks until
//     a single restorable snapshot lands under the bind-mounted snapshot dir so
//     the caller can tar it.
//   - continuous: `-d blobstore://...?bucket=... --snapshot_interval <seconds>`
//     without `-w`, which starts a streaming session against the SeaweedFS
//     object store and returns immediately. See
//     https://apple.github.io/foundationdb/backups.html
func runFDBBackupStart(ctx context.Context, b *backupCtx) error {
	cmd, binds, extraHosts, err := fdbBackupStartArgs(b)
	if err != nil {
		return err
	}
	res, err := runOneShot(ctx, b.Cli, b.Log, runOneShotOptions{
		Image:      b.Cfg.BackupFDBImage,
		Network:    b.Cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/fdbbackup"},
		Cmd:        cmd,
		Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
		Binds:      binds,
		ExtraHosts: extraHosts,
		Name:       "",
	})
	if err != nil {
		return err
	}
	b.Log.DebugContext(ctx, "backup.fdb.start_out", slog.String("output", res.Stdout))
	if res.ExitCode != 0 {
		return fmt.Errorf("fdbbackup start exited %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}
