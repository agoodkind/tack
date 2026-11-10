// The staleness check dates each backup mechanism's last success and prints
// one line per mechanism. It returns an error when any age exceeds its
// threshold or remains unknown. The alarm policy attempts mail before the
// error returns, unless a deputy defers to a recent primary run. A successful
// send suppresses repeated mail; failed sends permit later retries.
// Replication checks use
// the checking guest's own cluster observation.

package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupStalenessCheck reports the time since each mechanism's last
// success and returns an error for stale mechanisms.
//
// The report uses out for both fresh and stale readings. The alarm policy
// suppresses repeated mail after a successful send and can defer a deputy to
// a recent primary run. Failed sends permit later retries. Configuration and
// report-write failures can also return errors.
//
// FoundationDB checks follow the continuous-backup setting:
//
//   - Enabled continuous backup includes its restorable-point reading.
//   - Disabled continuous backup skips that reading, matching the restore
//     drill's FoundationDB selection.
func RunBackupStalenessCheck(ctx context.Context, cfg *config.Config, out io.Writer) error {
	if err := requireBackupStalenessConfig(ctx, cfg); err != nil {
		return err
	}

	metrics, writeErr := measureBackupStaleness(ctx, cfg, out)

	return backupStalenessVerdict(ctx, metrics, writeErr)
}

func requireBackupStalenessConfig(ctx context.Context, cfg *config.Config) error {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf(
			"staleness-check: object store configuration is incomplete; set " +
				"TACK_BACKUP_S3_ENDPOINT, TACK_BACKUP_S3_ACCESS_KEY_ID, and " +
				"TACK_BACKUP_S3_SECRET_ACCESS_KEY",
		)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.staleness.failed",
			slog.String("err", err.Error()),
		)

		return err
	}

	return nil
}

func measureBackupStaleness(
	ctx context.Context,
	cfg *config.Config,
	out io.Writer,
) ([]backupStalenessMetric, error) {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	logger.InfoContext(ctx, "backup.staleness.start", slog.String("bucket", cfg.BackupS3BucketMain))

	// Read the clock before each probe. The probes run in sequence and query an
	// object store, a cluster, and a container runtime. A slow probe can delay a
	// later sample past the future-timestamp tolerance if every sample uses one
	// earlier clock reading. That delay could misclassify a healthy sample as
	// corrupt.
	metrics := []backupStalenessMetric{
		exportStalenessMetric(ctx, cfg, s3Client, opsNow().UTC()),
		markerStalenessMetric(
			ctx,
			cfg,
			s3Client,
			backupStalenessRehearsalName,
			opsNow().UTC(),
			backupStalenessThreshold(cfg.BackupStalenessRehearsalMaxSeconds),
		),
		replicationStalenessMetric(ctx, cfg, s3Client, opsNow().UTC()),
	}
	if cfg.BackupFDBContinuous {
		metrics = append(metrics, fdbStalenessMetric(ctx, cfg, s3Client, opsNow().UTC()))
	} else {
		logger.WarnContext(
			ctx,
			"backup.staleness.metric_skipped",
			slog.String("metric", backupStalenessFDBName),
			slog.String("reason", "TACK_BACKUP_FDB_CONTINUOUS is false"),
		)
	}

	// Use this guest's last readable success to date a failed current reading.
	// A brief store or probe outage need not alarm before that success exceeds
	// the mechanism's threshold (TACK-515).
	metrics = rememberBackupStalenessReadings(ctx, cfg, metrics, opsNow().UTC())

	// Preserve a report-write error until the verdict. Log the metrics and
	// attempt alarms even if output fails. Return the write error only when
	// every mechanism is fresh.
	var writeErr error
	if _, err := io.WriteString(out, backupStalenessReport(metrics)); err != nil {
		writeErr = fmt.Errorf("write staleness report: %w", err)
		logger.ErrorContext(ctx, "backup.staleness.failed", slog.String("err", writeErr.Error()))
	}

	logBackupStalenessMetrics(ctx, metrics)

	// Process every reading for alarm transitions. A fresh current observation
	// clears its prior alarm state. A remembered fresh age does not clear a
	// current read failure. Mail failures do not replace the stale verdict.
	alarmBackupStalenessTransitions(ctx, cfg, s3Client, metrics)

	return metrics, writeErr
}

func backupStalenessVerdict(
	ctx context.Context,
	metrics []backupStalenessMetric,
	writeErr error,
) error {
	logger := telemetry.L(ctx)
	stale := staleBackupStalenessMetrics(metrics)
	if len(stale) > 0 {
		err := fmt.Errorf(
			"staleness-check: %d backup mechanism(s) past threshold: %s",
			len(stale),
			strings.Join(stale, ", "),
		)
		logger.ErrorContext(ctx, "backup.staleness.stale", slog.String("err", err.Error()))

		return err
	}

	if writeErr != nil {
		return writeErr
	}

	logger.InfoContext(ctx, "backup.staleness.ok", slog.Int("metric_count", len(metrics)))

	return nil
}

// logBackupStalenessMetrics records each metric's name, age, threshold, and
// verdict in one summary event. The log preserves the verdicts independently
// of alarm mail.
func logBackupStalenessMetrics(ctx context.Context, metrics []backupStalenessMetric) {
	readings := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		readings = append(
			readings,
			fmt.Sprintf(
				"%s age=%s threshold=%s %s",
				metric.Name,
				backupStalenessAgeField(metric),
				backupStalenessSeconds(metric.Threshold),
				metric.verdict(),
			),
		)
	}

	telemetry.L(ctx).InfoContext(ctx, "backup.staleness.metrics", slog.Any("readings", readings))
}

// exportStalenessMetric dates the off-host ledger export from the newest
// complete run's key. The key supplies a UTC timestamp, and the completeness
// check selects a restorable run without a separate marker. A listing or
// read failure reports an unknown age that this guest's last reading may
// date. A bucket without a complete run reports an unknown age and clears
// the remembered success.
func exportStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	now time.Time,
) backupStalenessMetric {
	logger := telemetry.L(ctx)
	threshold := backupStalenessThreshold(cfg.BackupStalenessExportMaxSeconds)
	runIDs, err := listYBSnapshotRunIDs(ctx, s3Client, cfg.BackupS3BucketMain)
	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessExportName,
			threshold,
			backupStoreReadCause(err),
			"listing export runs failed: "+err.Error(),
		)
	}

	manifest, found, err := newestCompleteYBSnapshotRun(
		runIDs,
		func(runID string) (ybSnapshotManifest, error) {
			return fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
		},
		func(key string) (bool, error) {
			return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
		func(runID, reason string) {
			logger.InfoContext(
				ctx,
				"backup.staleness.export_run_skipped",
				slog.String("run_id", runID),
				slog.String("reason", reason),
			)
		},
	)
	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessExportName,
			threshold,
			backupStoreReadCause(err),
			"walking export runs failed: "+err.Error(),
		)
	}

	if !found {
		return unknownBackupStalenessMetric(
			backupStalenessExportName,
			threshold,
			backupStalenessNeverRecorded,
			"no complete export run in "+cfg.BackupS3BucketMain,
		)
	}

	timestamp, err := time.Parse(ybSnapshotRunIDLayout, manifest.RunID)
	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessExportName,
			threshold,
			backupStalenessUnreadable,
			"export run key "+manifest.RunID+" is not a run-key timestamp",
		)
	}

	return knownBackupStalenessMetric(
		ctx,
		backupStalenessExportName,
		now,
		timestamp,
		threshold,
		"newest complete run "+manifest.RunID,
	)
}

// markerStalenessMetric dates a mechanism from its success marker. An absent
// marker does not provide a recorded success and produces a stale reading.
func markerStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	name string,
	now time.Time,
	threshold time.Duration,
) backupStalenessMetric {
	marker, found, err := readBackupStatusMarker(
		ctx,
		func(key string) ([]byte, error) {
			return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
		name,
	)
	if err != nil {
		return unknownBackupStalenessMetric(
			name,
			threshold,
			backupStoreReadCause(err),
			"reading "+backupStatusKey(name)+" failed: "+err.Error(),
		)
	}

	if !found {
		return unknownBackupStalenessMetric(
			name,
			threshold,
			backupStalenessNeverRecorded,
			"no "+backupStatusKey(name)+" in "+cfg.BackupS3BucketMain,
		)
	}

	return knownBackupStalenessMetric(ctx, name, now, marker.At, threshold, marker.Detail)
}

// fdbStalenessMetric dates continuous FoundationDB backup from its
// restorable point. It records the version and timestamp for conversion
// after total cluster loss, when the original cluster cannot convert the
// requested moment (TACK-468). The caller runs this probe only with
// continuous backup enabled. Disabled deployments do not construct its
// Docker client or one-shot container.
func fdbStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	now time.Time,
) backupStalenessMetric {
	logger := telemetry.L(ctx)
	threshold := backupStalenessThreshold(cfg.BackupStalenessFDBMaxSeconds)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessFDBName,
			threshold,
			backupStalenessUnreadable,
			"docker client unavailable: "+redactSecret(cfg, err.Error()),
		)
	}
	defer cli.Close()

	status, err := fdbBackupStatusText(
		ctx,
		&backupCtx{
			Cfg:     cfg,
			Cli:     cli,
			Log:     logger,
			RunID:   "",
			DestDir: "",
		},
	)
	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessFDBName,
			threshold,
			backupStalenessUnreadable,
			"fdbbackup status failed: "+redactSecret(cfg, err.Error()),
		)
	}

	point, err := fdbRestorablePointFromStatus(ctx, status)
	if errors.Is(err, errFDBNoRestorablePoint) {
		return unknownBackupStalenessMetric(
			backupStalenessFDBName,
			threshold,
			backupStalenessNeverRecorded,
			redactSecret(cfg, err.Error()),
		)
	}

	if err != nil {
		return unknownBackupStalenessMetric(
			backupStalenessFDBName,
			threshold,
			backupStalenessUnreadable,
			redactSecret(cfg, err.Error()),
		)
	}

	// Preserve the observed reading if recording its version fails. The writer
	// logs the failure. A missing lookup entry must not turn a valid restorable
	// point into a false staleness alarm.
	_ = appendFDBRestorablePoint(
		ctx,
		fdbVersionLogGetter(ctx, s3Client, cfg.BackupS3BucketMain),
		func(key string, body []byte) error {
			return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
		},
		point,
	)

	return knownBackupStalenessMetric(
		ctx,
		backupStalenessFDBName,
		now,
		point.At,
		threshold,
		"restorable through "+point.At.UTC().Format(time.RFC3339),
	)
}
