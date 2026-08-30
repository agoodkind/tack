package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

var errFDBBackupDestinationMismatch = errors.New(
	"active FoundationDB backup targets a different destination",
)

// backupCtx bundles runtime state for one FoundationDB continuous backup
// session without package-level state.
type backupCtx struct {
	Cfg     *config.Config
	Cli     *client.Client
	Log     *slog.Logger
	RunID   string
	DestDir string
}

// RunBackupFDBContinuousInit starts the FoundationDB continuous backup session
// that streams to the object store, and is safe to run repeatedly. The
// long-lived backup_agent that drains snapshots is the fdb-backup-agent compose
// service; this only starts the session. `ops provision` calls it on every run
// so a streaming environment arms itself, and the `fdb-continuous-init`
// subcommand exposes the same call for a deliberate out-of-band start. Both
// remain gated by TACK_BACKUP_FDB_CONTINUOUS=true.
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
// object store. A session already running on the configured destination is
// treated as success, so the call is idempotent. The blobstore credentials are
// embedded in the destination URL, so echoed output is redacted before logging.
func ensureFDBContinuousSession(ctx context.Context, b *backupCtx) error {
	// FoundationDB 7.4.6 rejects a duplicate start on the default tag. The
	// status guard makes repeated calls idempotent without issuing that start.
	active, statusErr := fdbBackupActive(ctx, b)
	if errors.Is(statusErr, errFDBBackupDestinationMismatch) {
		b.Log.ErrorContext(ctx, "backup.fdb.continuous_destination_mismatch",
			slog.String("err", statusErr.Error()))
		return statusErr
	}
	if statusErr == nil && active {
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

// fdbBackupStatusText runs `fdbbackup status` in a one-shot container against
// the live cluster and returns its combined output. The output embeds
// blobstore credentials in BackupURL, so callers keep it in memory and redact
// anything they log or print.
func fdbBackupStatusText(ctx context.Context, b *backupCtx) (string, error) {
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
		return "", err
	}
	return res.Stdout + "\n" + res.Stderr, nil
}

// fdbBackupActive reports whether a FoundationDB backup is currently running on
// the default tag and targets the configured bucket and endpoint host. The
// status output embeds blobstore credentials in BackupURL, so the URL is only
// compared in memory. An infrastructure error returns false so the caller can
// proceed to start, while an active backup at another destination returns an
// error.
func fdbBackupActive(ctx context.Context, b *backupCtx) (bool, error) {
	status, err := fdbBackupStatusText(ctx, b)
	if err != nil {
		return false, err
	}
	return fdbBackupActiveFromStatus(b.Cfg, status)
}

func fdbBackupActiveFromStatus(cfg *config.Config, status string) (bool, error) {
	out := strings.ToLower(status)
	active := strings.Contains(out, "is in progress") ||
		strings.Contains(out, "restorable but continuing") ||
		strings.Contains(out, "is running")
	if !active {
		return false, nil
	}

	backupURL, found := fdbBackupURLFromStatus(status)
	if !found {
		return false, errFDBBackupDestinationMismatch
	}
	if !fdbBackupURLMatchesDestination(cfg, backupURL) {
		return false, errFDBBackupDestinationMismatch
	}
	return true, nil
}

func fdbBackupURLFromStatus(status string) (string, bool) {
	for line := range strings.SplitSeq(status, "\n") {
		key, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(key), "BackupURL") {
			backupURL := strings.TrimSpace(value)
			return backupURL, backupURL != ""
		}
	}
	return "", false
}

func fdbBackupURLMatchesDestination(cfg *config.Config, backupURL string) bool {
	parsedBackupURL, err := url.Parse(backupURL)
	if err != nil {
		return false
	}
	configuredHost, _, err := blobstoreHostMapping(cfg.BackupS3Endpoint)
	if err != nil {
		return false
	}
	parsedConfiguredHost, err := url.Parse("blobstore://" + configuredHost)
	if err != nil {
		return false
	}

	// Compare the full authority (host and port), not just the hostname, so an
	// active backup on the configured host and bucket but a different port is
	// rejected rather than silently accepted.
	authorityMatches := strings.EqualFold(
		parsedBackupURL.Host,
		parsedConfiguredHost.Host,
	)
	bucketMatches := parsedBackupURL.Query().Get("bucket") == cfg.BackupS3BucketMain
	return parsedBackupURL.Scheme == "blobstore" && authorityMatches && bucketMatches
}
