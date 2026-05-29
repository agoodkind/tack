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

// runBackupFDB performs a `fdbbackup`-driven snapshot. The 2026-05-09
// rebuild proved that a named-volume tar of /var/fdb/data is silently
// empty because the FDB image declares `VOLUME /var/fdb/data` and an
// anonymous volume shadows the bind. The fix: drive an actual fdbbackup
// session against the live cluster, with a backup_agent sidecar to drain
// the queue, then assert `Restorable: true` against the timestamped
// subdirectory before we trust the artifact.
//
// All four 2026-05-09 fixes preserved:
//   - fdbbackup, not volume tar (anonymous-volume shadowing)
//   - backup_agent sidecar with `--entrypoint /usr/bin/backup_agent`
//   - fdbbackup describe targeting the resolved `backup-*` subdir
//   - assert `Restorable: true` or fail loudly
func runBackupFDB(ctx context.Context, b *backupCtx) error {
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

	// Continuous backups stream straight to object storage; there is no local
	// snapshot subdir to resolve, describe, or tar. The fdbbackup session keeps
	// running after this command returns and is verified out of band against the
	// object store. See https://apple.github.io/foundationdb/backups.html
	if b.Cfg.BackupFDBContinuous {
		b.Log.InfoContext(ctx, "backup.fdb.continuous_started",
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
			slog.String("endpoint", b.Cfg.BackupS3Endpoint),
			slog.Int("snapshot_interval_seconds", b.Cfg.BackupFDBSnapshotInterval),
		)
		return nil
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
	cmd, binds, err := fdbBackupStartArgs(b)
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
