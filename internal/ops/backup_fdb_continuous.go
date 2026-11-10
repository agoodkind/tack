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

// backupCtx groups runtime state for one FoundationDB continuous backup
// session without using package-level state.
type backupCtx struct {
	Cfg     *config.Config
	Cli     *client.Client
	Log     *slog.Logger
	RunID   string
	DestDir string
}

// RunBackupFDBContinuousInit starts a FoundationDB continuous backup session
// that writes to the object store. Repeated calls accept an existing session
// at the configured destination.
//
// The fdb-backup-agent Compose service runs the long-lived backup_agent that
// processes snapshots. This function starts the session rather than the agent.
//
// The same TACK_BACKUP_FDB_CONTINUOUS=true gate applies to both callers:
//   - The ops provision command initializes the session on every run.
//   - The fdb-continuous-init subcommand permits an explicit separate start.
func RunBackupFDBContinuousInit(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if !cfg.BackupFDBContinuous {
		err := fmt.Errorf("fdb-continuous-init requires TACK_BACKUP_FDB_CONTINUOUS=true")
		logger.ErrorContext(
			ctx,
			"backup.fdb.continuous_init_disabled",
			slog.String("err", err.Error()),
		)

		return err
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	// The continuous session does not use DestDir. It writes to the object store
	// and binds only the cluster file.
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

// ensureFDBContinuousSession starts a continuous fdbbackup session to the
// object store. An active session at the configured destination counts as
// success. The destination URL includes credentials, which the function
// redacts from echoed output before logging.
func ensureFDBContinuousSession(ctx context.Context, b *backupCtx) error {
	// FoundationDB 7.4.6 rejects a duplicate start on the default tag. The status
	// check accepts an existing session without issuing another start.
	active, statusErr := fdbBackupActive(ctx, b)
	if errors.Is(statusErr, errFDBBackupDestinationMismatch) {
		b.Log.ErrorContext(
			ctx,
			"backup.fdb.continuous_destination_mismatch",
			slog.String("err", statusErr.Error()),
		)

		return statusErr
	}

	if statusErr == nil && active {
		b.Log.InfoContext(
			ctx,
			"backup.fdb.continuous_already_running",
			slog.String("bucket", b.Cfg.BackupS3BucketMain),
		)

		return nil
	}

	cmd, binds, extraHosts, err := fdbBackupStartArgs(b)
	if err != nil {
		return err
	}

	res, err := runOneShot(
		ctx,
		b.Cli,
		b.Log,
		runOneShotOptions{
			Image:      b.Cfg.BackupFDBImage,
			Network:    b.Cfg.BackupFDBNetwork,
			Entrypoint: []string{"/usr/bin/fdbbackup"},
			Cmd:        cmd,
			Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
			Binds:      binds,
			ExtraHosts: extraHosts,
			Name:       "",
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("run fdbbackup start: %w", err)
		b.Log.ErrorContext(
			ctx,
			"backup.fdb.continuous_start_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if res.ExitCode != 0 {
		combined := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
		wrapped := fmt.Errorf(
			"fdbbackup start exited %d: %s",
			res.ExitCode,
			redactSecret(b.Cfg, combined),
		)
		b.Log.ErrorContext(
			ctx,
			"backup.fdb.continuous_start_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	b.Log.InfoContext(
		ctx,
		"backup.fdb.continuous_started",
		slog.String("bucket", b.Cfg.BackupS3BucketMain),
		slog.String("endpoint", b.Cfg.BackupS3Endpoint),
		slog.Int("snapshot_interval_seconds", b.Cfg.BackupFDBSnapshotInterval),
	)

	return nil
}

// fdbBackupStatusText runs fdbbackup status against the live cluster in a
// one-shot container and returns the combined output. BackupURL includes
// blobstore credentials. Callers must retain it in memory and redact any
// output they log or print.
func fdbBackupStatusText(ctx context.Context, b *backupCtx) (string, error) {
	res, err := runOneShot(
		ctx,
		b.Cli,
		b.Log,
		runOneShotOptions{
			Image:      b.Cfg.BackupFDBImage,
			Network:    b.Cfg.BackupFDBNetwork,
			Entrypoint: []string{"/usr/bin/fdbbackup"},
			Cmd:        []string{"status", "-C", "/etc/foundationdb/fdb.cluster"},
			Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
			Binds:      []string{fdbClusterReadOnlyBind(b.Cfg)},
			ExtraHosts: nil,
			Name:       "",
		},
	)
	if err != nil {
		return "", err
	}

	return res.Stdout + "\n" + res.Stderr, nil
}

// fdbBackupActive checks whether the default-tag backup runs at the
// configured bucket and endpoint host. It compares BackupURL only in memory
// because the status output includes credentials. An infrastructure error
// returns false and an error; the caller can then attempt a start. An active
// backup at another destination returns a destination-mismatch error.
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

	// Compare the full authority, including the port. An active backup with the
	// configured hostname and bucket but a different port must fail this check.
	authorityMatches := strings.EqualFold(parsedBackupURL.Host, parsedConfiguredHost.Host)
	bucketMatches := parsedBackupURL.Query().Get("bucket") == cfg.BackupS3BucketMain

	return parsedBackupURL.Scheme == "blobstore" && authorityMatches && bucketMatches
}
