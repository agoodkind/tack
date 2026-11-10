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

const fdbScratchClusterFileWait = 30 * time.Second

// restoreDrillFDB restores the continuous FoundationDB backup from the
// object store into a temporary standalone cluster and checks that the
// keyspace contains data. The scratch cluster uses separate data and a
// container-mode coordinator without loopback. It never joins the live
// cluster. The function discovers backup names in the bucket rather than a
// local pointer.
//
// r.FDBTargetTime selects the restore moment. Nil selects the newest backup's
// latest restorable point. An explicit target selects the newest backup with
// a covering window before the restore starts. If every window excludes the
// target, the drill fails rather than restoring the latest point.
func restoreDrillFDB(ctx context.Context, r *restoreDrillCtx) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.fdb.start")
	if r.Cfg.BackupFDBOverlayPath == "" {
		err := fmt.Errorf("restore-drill fdb: TACK_BACKUP_FDB_OVERLAY_PATH is required")
		logger.ErrorContext(ctx, "backup.restore_drill.fdb.failed", slog.String("err", err.Error()))

		return err
	}

	s3Client := newBackupS3Client(r.Cfg)
	// FoundationDB registers each backup with a marker object at backups/<name>.
	// List those markers rather than the engine's internal backups/ subfolder.
	// Selection strips the prefix to obtain the bare name; fdbrestore adds the
	// backups/ folder again.
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

	// Monitor restore progress without a fixed total duration. Progress permits
	// the restore to continue for the dataset's required time. A stall fails the
	// drill with the last progress report. Each probe has its own timeout to
	// prevent a hung exec from blocking the monitor.
	err = waitFDBDrillRestore(ctx, r, name, logger)
	if err != nil {
		return err
	}

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

// bootScratchFDB starts standalone FoundationDB in container mode with the
// IPv6 overlay, configures a new single-ssd database, and starts backup_agent
// for fdbrestore to read the blobstore snapshot. The scratch cluster uses
// separate data and never joins the live cluster. Fresh data and log
// directories use the backup root for each run. The rest of /var/fdb remains
// from the image, including its scripts.
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
		// fdbrestore resolves a wall-clock target against the source cluster.
		// Mount the live cluster file read-only only for an explicit time target.
		binds = append(binds, fdbOrigClusterMount)
	}

	containerID, err := createScratchFDB(ctx, r, name, binds, extraHosts, logger)
	if err != nil {
		return err
	}

	r.trackContainer(name)
	if _, err := r.Cli.ContainerStart(
		ctx,
		containerID,
		client.ContainerStartOptions{},
	); err != nil {
		wrapped := fmt.Errorf("start scratch fdb %s: %w", name, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	err = initializeScratchFDB(ctx, r, name, logger)
	if err != nil {
		return err
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
			"mkdir -p /var/fdb/logs; backup_agent -C /var/fdb/fdb.cluster " +
				"--logdir /var/fdb/logs >/var/fdb/logs/agent.out 2>&1 &",
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

func waitFDBDrillRestore(
	ctx context.Context,
	drill *restoreDrillCtx,
	name string,
	logger *slog.Logger,
) error {
	progress, exitCode, err := awaitFDBRestore(
		ctx,
		fdbRestoreWatch{
			Finished: func(pollCtx context.Context) (int, bool, error) {
				return fdbRestoreExitCode(pollCtx, drill, name)
			},
			Status: func(pollCtx context.Context) (string, error) {
				return fdbRestoreStatusText(pollCtx, drill, name)
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
			fdbRestoreLogTail(ctx, drill, name),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.fdb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if exitCode != 0 {
		wrapped := &fdbRestoreExitError{
			exitCode: exitCode,
			progress: progress.summary(),
			logTail:  fdbRestoreLogTail(ctx, drill, name),
		}
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

	return nil
}

func createScratchFDB(
	ctx context.Context,
	drill *restoreDrillCtx,
	name string,
	binds []string,
	extraHosts []string,
	logger *slog.Logger,
) (string, error) {
	created, err := drill.Cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{
				Image: drill.Cfg.BackupFDBImage,
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
			NetworkingConfig: netMode(drill.Cfg.BackupFDBNetwork),
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

		return "", wrapped
	}

	return created.ID, nil
}

func initializeScratchFDB(
	ctx context.Context,
	drill *restoreDrillCtx,
	name string,
	logger *slog.Logger,
) error {
	if err := waitExecOK(
		ctx,
		drill,
		name,
		fdbScratchClusterFileWait,
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
		drill.Cli,
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

	return nil
}
