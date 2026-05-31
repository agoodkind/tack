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

// redactSecret removes the S3 secret from text before it is logged, because
// fdbbackup and fdbrestore echo the blobstore URL with inline credentials.
func redactSecret(cfg *config.Config, s string) string {
	if cfg.BackupS3SecretKey == "" {
		return s
	}
	return strings.ReplaceAll(s, cfg.BackupS3SecretKey, "***REDACTED***")
}

// restoreDrillFDB restores the FoundationDB continuous backup from the object
// store into a throwaway standalone cluster and asserts the keyspace is
// non-empty. The scratch cluster uses its own data and a loopback-free
// container-mode coordinator; it never joins the live cluster. The backup name
// is discovered from the bucket, not a local pointer.
func restoreDrillFDB(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.fdb.start")
	if r.Cfg.BackupFDBOverlayPath == "" {
		err := fmt.Errorf("restore-drill fdb: TACK_BACKUP_FDB_OVERLAY_PATH is required")
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))
		return err
	}

	s3Client := newBackupS3Client(r.Cfg)
	names, err := listImmediatePrefixes(ctx, s3Client, r.Cfg.BackupS3BucketMain, "backups/")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		wrapped := fmt.Errorf("no FDB backup under backups/ in bucket %s", r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.no_backup", slog.String("err", wrapped.Error()))
		return wrapped
	}
	// Latest by sortable name. fdbrestore fails clearly if it is not restorable.
	backupName := strings.TrimSuffix(names[len(names)-1], "/")
	backupName = strings.TrimPrefix(backupName, "backups/")
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

	restoreRes, err := containerExec(ctx, r.Cli, name, []string{
		"sh", "-c",
		fmt.Sprintf("timeout %d fdbrestore start --dest-cluster-file /var/fdb/fdb.cluster -r '%s' --waitfordone",
			r.Cfg.BackupFDBTimeoutSeconds, dest),
	})
	if err != nil {
		return fmt.Errorf("fdbrestore exec: %w", err)
	}
	if restoreRes.ExitCode != 0 {
		wrapped := fmt.Errorf("fdbrestore exited %d: %s", restoreRes.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(restoreRes.Stdout+" "+restoreRes.Stderr)))
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

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
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: r.Cfg.BackupFDBImage,
			Env:   []string{"FDB_NETWORKING_MODE=container", "FDB_PORT=4500", "FDB_PROCESS_CLASS=unset"},
		},
		HostConfig: &container.HostConfig{
			Binds: []string{
				volume + ":/var/fdb",
				r.Cfg.BackupFDBOverlayPath + ":/var/fdb/scripts/fdb.bash:ro",
			},
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
