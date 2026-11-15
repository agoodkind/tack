package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/telemetry"
)

// restoreDrillFDB discovers a FoundationDB backup in the bucket, restores it
// into a standalone scratch cluster, and checks for a nonempty keyspace.
// The scratch cluster uses separate data and a container-mode coordinator
// without loopback addresses. It never joins the live cluster.
//
// Without r.FDBTargetTime, the drill restores the newest backup's latest point.
// With a target, it selects the newest backup that can restore that moment.
// The drill refuses targets outside every backup's window before restoring.
func restoreDrillFDB(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.fdb.start")
	if r.Cfg.BackupFDBOverlayPath == "" {
		err := fmt.Errorf("restore-drill fdb: TACK_BACKUP_FDB_OVERLAY_PATH is required")
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))

		return err
	}

	s3Client := newBackupS3Client(r.Cfg)
	// FoundationDB registers backup markers at backups/<name>. Discovery lists
	// these markers, not the engine's internal backups/ subfolder. Selection
	// strips the prefix because fdbrestore adds it to the backup name.
	markers, err := listImmediateObjects(
		ctx,
		s3Client,
		r.Cfg.BackupS3BucketMain,
		fdbBackupMarkerPrefix,
	)
	if err != nil {
		return err
	}

	if len(markers) == 0 {
		wrapped := fmt.Errorf("no FDB backup under backups/ in bucket %s", r.Cfg.BackupS3BucketMain)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.no_backup",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	extraHosts, err := blobstoreExtraHosts(r.Cfg.BackupS3Endpoint)
	if err != nil {
		return err
	}

	name := "tack-rtfdb-" + r.RunID
	if err := bootScratchFDB(ctx, r, name, extraHosts); err != nil {
		return err
	}

	backupName, err := selectFDBBackup(ctx, r, name, markers)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "backup.restore_drill.fdb.backup", slog.String("name", backupName))
	dest, err := fdbBlobstoreURL(r.Cfg, backupName)
	if err != nil {
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

	// Monitoring permits continued work without an overall deadline and fails
	// on inactivity with the last recorded progress. Each probe has its own
	// deadline to prevent a hung exec from blocking stall detection.
	progress, exitCode, err := awaitFDBRestore(
		ctx,
		fdbRestoreWatch{
			Finished: func(pollCtx context.Context) (int, bool, error) {
				return fdbRestoreExitCode(pollCtx, r, name)
			},
			Status: func(pollCtx context.Context) (string, error) {
				return fdbRestoreStatusText(pollCtx, r, name)
			},
		},
		fdbDrillRestoreStallWindow,
		fdbDrillRestorePollInterval,
		fdbDrillProbeTimeout,
	)
	if err != nil {
		wrapped := fmt.Errorf(
			"%w; the restore's last output was: %s",
			err,
			fdbRestoreLogTail(ctx, r, name),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if exitCode != 0 {
		wrapped := fmt.Errorf(
			"fdbrestore exited %d after %s: %s",
			exitCode,
			progress.summary(),
			fdbRestoreLogTail(ctx, r, name),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.fdb.restored",
		slog.String("progress", progress.summary()),
	)

	assertRes, err := containerExec(
		ctx,
		r.Cli,
		name,
		[]string{"fdbcli", "-C", "/var/fdb/fdb.cluster", "--exec", `getrange "" \xff 5`},
	)
	if err != nil {
		return fmt.Errorf("fdb getrange exec: %w", err)
	}

	if !strings.Contains(assertRes.Stdout, "`") {
		wrapped := fmt.Errorf("restored FoundationDB keyspace is empty")
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(ctx, "backup.restore_drill.fdb.ok", slog.String("name", backupName))

	return nil
}

// bootScratchFDB starts a standalone container-mode cluster with the IPv6
// overlay, configures a single-ssd database, and starts backup_agent for
// restore. The cluster never joins the live cluster. Each run uses fresh
// data and log directories under the backup root. Other paths under /var/fdb
// retain the image's contents, including its scripts.
func bootScratchFDB(
	ctx context.Context,
	r *restoreDrillCtx,
	name string,
	extraHosts []string,
) error {
	logger := telemetry.L(ctx)
	if err := ensureImage(ctx, r.Cli, logger, r.Cfg.BackupFDBImage); err != nil {
		return err
	}

	dataDir, err := makeDrillScratchDir(ctx, r, r.Cfg.BackupFDBImage, "fdb", "data")
	if err != nil {
		return err
	}

	logsDir, err := makeDrillScratchDir(ctx, r, r.Cfg.BackupFDBImage, "fdb", "logs")
	if err != nil {
		return err
	}

	binds := []string{
		dataDir + ":/var/fdb/data",
		logsDir + ":/var/fdb/logs",
		r.Cfg.BackupFDBOverlayPath + ":/var/fdb/scripts/fdb.bash:ro",
	}
	if r.FDBTargetTime != nil {
		// Only point-in-time restores read the live cluster file. fdbrestore
		// resolves the timestamp against that cluster through a read-only bind.
		binds = append(binds, fdbOrigClusterMount)
	}

	created, err := r.Cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{
				Image: r.Cfg.BackupFDBImage,
				Env: []string{
					"FDB_NETWORKING_MODE=container",
					"FDB_PORT=4500",
					"FDB_PROCESS_CLASS=unset",
				},
			},
			HostConfig: &container.HostConfig{
				Binds:      binds,
				ExtraHosts: extraHosts,
			},
			NetworkingConfig: netMode(r.Cfg.BackupFDBNetwork),
			Name:             name,
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("create scratch fdb %s: %w", name, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	r.trackContainer(name)
	if _, err := r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		wrapped := fmt.Errorf("start scratch fdb %s: %w", name, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if err := waitExecOK(
		ctx,
		r,
		name,
		30*time.Second,
		nil,
		[]string{"test", "-f", "/var/fdb/fdb.cluster"},
	); err != nil {
		wrapped := fmt.Errorf("scratch fdb cluster file never appeared: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if _, err := containerExec(
		ctx,
		r.Cli,
		name,
		[]string{
			"fdbcli",
			"-C",
			"/var/fdb/fdb.cluster",
			"--exec",
			"configure new single ssd",
		},
	); err != nil {
		wrapped := fmt.Errorf("configure new single ssd: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if err := waitExecOK(
		ctx,
		r,
		name,
		60*time.Second,
		nil,
		[]string{
			"sh",
			"-c",
			"fdbcli -C /var/fdb/fdb.cluster --exec 'status minimal' | grep -q available",
		},
	); err != nil {
		wrapped := fmt.Errorf("scratch fdb never reported available: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if _, err := containerExec(
		ctx,
		r.Cli,
		name,
		[]string{
			"sh",
			"-c",
			"mkdir -p /var/fdb/logs; backup_agent -C /var/fdb/fdb.cluster --logdir /var/fdb/logs >/var/fdb/logs/agent.out 2>&1 &",
		},
	); err != nil {
		wrapped := fmt.Errorf("start scratch backup_agent: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}
