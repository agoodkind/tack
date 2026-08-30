package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// redactSecret removes both S3 credentials from text before it is logged,
// because fdbbackup and fdbrestore echo the blobstore URL inline.
func redactSecret(cfg *config.Config, s string) string {
	first := cfg.BackupS3SecretKey
	second := cfg.BackupS3AccessKey
	if len(second) > len(first) {
		first, second = second, first
	}
	for _, credential := range []string{first, second} {
		if credential != "" {
			s = strings.ReplaceAll(s, credential, "***REDACTED***")
		}
	}
	return s
}

// restoreDrillFDB restores the FoundationDB continuous backup from the object
// store into a throwaway standalone cluster and asserts the keyspace is
// non-empty. The scratch cluster uses its own data and a loopback-free
// container-mode coordinator; it never joins the live cluster. The backup name
// is discovered from the bucket, not a local pointer.
//
// r.FDBTargetTime selects the moment to restore to. No target restores the
// latest restorable point. A target is checked against the backup's restorable
// window before any restore starts, so a moment the backup cannot reach stops
// the drill instead of quietly restoring the latest.
func restoreDrillFDB(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.fdb.start")
	if r.Cfg.BackupFDBOverlayPath == "" {
		err := fmt.Errorf("restore-drill fdb: TACK_BACKUP_FDB_OVERLAY_PATH is required")
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return err
	}

	s3Client := newBackupS3Client(r.Cfg)
	// The FoundationDB blobstore registers each backup as a marker object at
	// backups/<name>, so the markers are listed (not the engine's internal
	// backups/ subfolder) to discover backups.
	names, err := listImmediateObjects(ctx, s3Client, r.Cfg.BackupS3BucketMain, "backups/")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		wrapped := fmt.Errorf("no FDB backup under backups/ in bucket %s", r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.no_backup", slog.String("err", wrapped.Error()))
		return wrapped
	}
	// Latest marker by sortable key. fdbrestore re-adds the backups/ folder from
	// the backup name, so the backups/ prefix is stripped here to recover the bare
	// name fdbrestore addresses. fdbrestore fails clearly if it is not restorable.
	backupName := strings.TrimPrefix(names[len(names)-1], "backups/")
	logger.InfoContext(ctx, "backup.restore_drill.fdb.backup", slog.String("name", backupName))

	dest, err := fdbBlobstoreURL(r.Cfg, backupName)
	if err != nil {
		return err
	}
	extraHosts, err := blobstoreExtraHosts(r.Cfg.BackupS3Endpoint)
	if err != nil {
		return err
	}

	name := "tack-rtfdb-" + r.RunID
	if err := bootScratchFDB(ctx, r, name, extraHosts); err != nil {
		return err
	}

	if err := assertFDBTargetRestorable(ctx, r, name, dest); err != nil {
		return err
	}

	restoreCmd, err := fdbRestoreCommand(dest, r.FDBTargetTime)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return err
	}
	if err := launchFDBRestore(ctx, r, name, restoreCmd); err != nil {
		return err
	}
	// The restore is watched for progress, not timed. A restore that keeps
	// moving runs as long as its dataset needs; only one that stops moving
	// fails the drill, and it fails naming what it had done when it stopped.
	progress, exitCode, err := awaitFDBRestore(ctx, fdbRestoreWatch{
		Finished: func(pollCtx context.Context) (int, bool, error) {
			return fdbRestoreExitCode(pollCtx, r, name)
		},
		Status: func(pollCtx context.Context) (string, error) {
			return fdbRestoreStatusText(pollCtx, r, name)
		},
	}, fdbDrillRestoreStallWindow, fdbDrillRestorePollInterval)
	if err != nil {
		wrapped := fmt.Errorf("%w; the restore's last output was: %s", err, fdbRestoreLogTail(ctx, r, name))
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("fdbrestore exited %d after %s: %s",
			exitCode, progress.summary(), fdbRestoreLogTail(ctx, r, name))
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.fdb.restored", slog.String("progress", progress.summary()))

	assertRes, err := containerExec(ctx, r.Cli, name,
		[]string{"fdbcli", "-C", "/var/fdb/fdb.cluster", "--exec", `getrange "" \xff 5`})
	if err != nil {
		return fmt.Errorf("fdb getrange exec: %w", err)
	}
	if !strings.Contains(assertRes.Stdout, "`") {
		wrapped := fmt.Errorf("restored FoundationDB keyspace is empty")
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.fdb.ok", slog.String("name", backupName))
	return nil
}

// bootScratchFDB creates a fresh volume and boots a standalone container-mode
// FoundationDB with the IPv6 overlay, configures a new single-ssd database, and
// starts a backup_agent so fdbrestore can drain the blobstore snapshot. The
// scratch cluster has its own data and never joins the live cluster.
func bootScratchFDB(ctx context.Context, r *restoreDrillCtx, name string, extraHosts []string) error {
	logger := telemetry.L(ctx)
	volume := "tack-rtfdb-data-" + r.RunID
	if _, err := r.Cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume}); err != nil {
		wrapped := fmt.Errorf("create scratch fdb volume %s: %w", volume, err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	r.trackVolume(volume)

	if err := ensureImage(ctx, r.Cli, logger, r.Cfg.BackupFDBImage); err != nil {
		return err
	}
	binds := []string{
		volume + ":/var/fdb",
		r.Cfg.BackupFDBOverlayPath + ":/var/fdb/scripts/fdb.bash:ro",
	}
	if r.FDBTargetTime != nil {
		// fdbrestore resolves a wall-clock target against the source cluster,
		// so the live cluster's file has to be readable in here. Read-only,
		// and only for a run that asked for a point in time.
		binds = append(binds, fdbOrigClusterMount)
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: r.Cfg.BackupFDBImage,
			Env:   []string{"FDB_NETWORKING_MODE=container", "FDB_PORT=4500", "FDB_PROCESS_CLASS=unset"},
		},
		HostConfig: &container.HostConfig{
			Binds:      binds,
			ExtraHosts: extraHosts,
		},
		NetworkingConfig: netMode(r.Cfg.BackupFDBNetwork),
		Name:             name,
	})
	if err != nil {
		wrapped := fmt.Errorf("create scratch fdb %s: %w", name, err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	r.trackContainer(name)
	if _, err := r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		wrapped := fmt.Errorf("start scratch fdb %s: %w", name, err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	if err := waitExecOK(ctx, r, name, 30*time.Second, nil, []string{"test", "-f", "/var/fdb/fdb.cluster"}); err != nil {
		wrapped := fmt.Errorf("scratch fdb cluster file never appeared: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if _, err := containerExec(ctx, r.Cli, name,
		[]string{"fdbcli", "-C", "/var/fdb/fdb.cluster", "--exec", "configure new single ssd"}); err != nil {
		wrapped := fmt.Errorf("configure new single ssd: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := waitExecOK(ctx, r, name, 60*time.Second, nil,
		[]string{"sh", "-c", "fdbcli -C /var/fdb/fdb.cluster --exec 'status minimal' | grep -q available"}); err != nil {
		wrapped := fmt.Errorf("scratch fdb never reported available: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	if _, err := containerExec(ctx, r.Cli, name,
		[]string{"sh", "-c", "mkdir -p /var/fdb/logs; backup_agent -C /var/fdb/fdb.cluster --logdir /var/fdb/logs >/var/fdb/logs/agent.out 2>&1 &"}); err != nil {
		wrapped := fmt.Errorf("start scratch backup_agent: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
