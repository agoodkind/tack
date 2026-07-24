package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupFDBContinuousInit starts the FoundationDB continuous backup session
// that streams to the object store, and is safe to run repeatedly. The
// long-lived backup_agent that drains snapshots is the fdb-backup-agent compose
// service; this command only starts the session. The command remains gated by
// TACK_BACKUP_FDB_CONTINUOUS=true.
func RunBackupFDBContinuousInit(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if !cfg.BackupFDBContinuous {
		err := fmt.Errorf("fdb-continuous-init requires TACK_BACKUP_FDB_CONTINUOUS=true")
		logger.ErrorContext(ctx, "backup.fdb.continuous_init_disabled", slog.String("err", err.Error()))
		return err
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	// DestDir is unused by the continuous session, which streams to the object
	// store and binds only the cluster file.
	b := &backupCtx{
		Cfg:     cfg,
		Cli:     cli,
		Log:     logger,
		RunID:   opsNow().UTC().Format("20060102T150405Z"),
		DestDir: "",
	}
	logger.InfoContext(ctx, "backup.fdb.continuous_init.start", slog.String("run_id", b.RunID))
	return ensureFDBContinuousSession(ctx, b)
}

// ensureFDBContinuousSession starts the streaming fdbbackup session to the
// object store. A session already running on the default tag is treated as
// success, so the call is idempotent. The blobstore secret is embedded in the
// destination URL, so echoed output is redacted before it is logged.
func ensureFDBContinuousSession(ctx context.Context, b *backupCtx) error {
	// Idempotent: fdbbackup start replaces a running backup rather than erroring,
	// so a status check guards against discontinuing an active continuous backup
	// and starting a fresh one on every call.
	if active, statusErr := fdbBackupActive(ctx, b); statusErr == nil && active {
		b.Log.InfoContext(ctx, "backup.fdb.continuous_already_running",
			slog.String("bucket", b.Cfg.BackupS3BucketMain))
		return nil
	}

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
		wrapped := fmt.Errorf("run fdbbackup start: %w", err)
		b.Log.ErrorContext(ctx, "backup.fdb.continuous_start_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if res.ExitCode != 0 {
		combined := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
		wrapped := fmt.Errorf("fdbbackup start exited %d: %s", res.ExitCode, redactSecret(b.Cfg, combined))
		b.Log.ErrorContext(ctx, "backup.fdb.continuous_start_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	b.Log.InfoContext(ctx, "backup.fdb.continuous_started",
		slog.String("bucket", b.Cfg.BackupS3BucketMain),
		slog.String("endpoint", b.Cfg.BackupS3Endpoint),
		slog.Int("snapshot_interval_seconds", b.Cfg.BackupFDBSnapshotInterval),
	)
	return nil
}

// fdbBackupActive reports whether a FoundationDB backup is currently running on
// the default tag, read from fdbbackup status. The status output embeds the
// blobstore secret in the backup URL, so it is never logged; only the boolean is
// used. An infrastructure error returns false so the caller proceeds to start.
func fdbBackupActive(ctx context.Context, b *backupCtx) (bool, error) {
	res, err := runOneShot(ctx, b.Cli, b.Log, runOneShotOptions{
		Image:      b.Cfg.BackupFDBImage,
		Network:    b.Cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/fdbbackup"},
		Cmd:        []string{"status", "-C", "/etc/foundationdb/fdb.cluster"},
		Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
		Binds:      []string{"/etc/foundationdb:/etc/foundationdb:ro"},
		ExtraHosts: nil,
		Name:       "",
	})
	if err != nil {
		return false, err
	}
	out := strings.ToLower(res.Stdout + " " + res.Stderr)
	return strings.Contains(out, "is in progress") ||
		strings.Contains(out, "restorable but continuing") ||
		strings.Contains(out, "is running"), nil
}
