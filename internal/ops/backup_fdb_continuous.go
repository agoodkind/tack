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

// backupCtx stores runtime state for one continuous backup session.
type backupCtx struct {
	Cfg     *config.Config
	Cli     *client.Client
	Log     *slog.Logger
	RunID   string
	DestDir string
}

// RunBackupFDBContinuousInit starts an idempotent object-store backup session.
// The fdb-backup-agent service performs ongoing snapshot work. Provisioning and
// explicit fdb-continuous-init both require TACK_BACKUP_FDB_CONTINUOUS=true.
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

	// Direct object-store backup does not use DestDir and binds only the
	// cluster file rather than a staged backup directory.
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

// ensureFDBContinuousSession accepts an active backup at the configured
// destination without another start. Destination URLs include credentials;
// echoed command output requires redaction before logging.
func ensureFDBContinuousSession(ctx context.Context, b *backupCtx) error {
	// FoundationDB 7.4.6 rejects duplicate starts on the default tag.
	// The status check prevents issuing that start during repeated calls.
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

// fdbBackupStatusText reads combined live-cluster status. BackupURL includes
// credentials; callers must redact output before logging or printing it.
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

// fdbBackupActive checks the default tag's active destination in memory without
// logging credential-bearing URLs. Infrastructure failures permit a subsequent
// start attempt; active backups at different destinations return a refusal.
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

	// Full authority comparison includes the port. Matching only hostname and
	// bucket could incorrectly accept an active backup on another port.
	authorityMatches := strings.EqualFold(parsedBackupURL.Host, parsedConfiguredHost.Host)
	bucketMatches := parsedBackupURL.Query().Get("bucket") == cfg.BackupS3BucketMain

	return parsedBackupURL.Scheme == "blobstore" && authorityMatches && bucketMatches
}
