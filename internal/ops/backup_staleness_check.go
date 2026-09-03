// backup_staleness_check.go runs the staleness check: it dates every backup
// mechanism's last success, writes the replication marker whenever it observes
// the cluster healthy, prints one line per mechanism, and exits nonzero when
// any age is past its threshold. The run that first finds a mechanism stale
// mails a plain-words account of the fault before returning that error, so a
// backup that quietly stopped producing anything becomes one message rather
// than a discovery during a restore.

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

// RunBackupStalenessCheck reports how long ago each backup mechanism last
// succeeded and returns an error when any mechanism is past its threshold. The
// report goes to out whether or not anything is stale; a mechanism that has
// just become stale is mailed once, in plain words. The FoundationDB leg is
// skipped when continuous backup is disabled, the same way the restore drill
// skips its FoundationDB leg.
func RunBackupStalenessCheck(ctx context.Context, cfg *config.Config, out io.Writer) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("staleness-check: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.staleness.failed", slog.String("err", err.Error()))
		return err
	}

	s3Client := newBackupS3Client(cfg)
	logger.InfoContext(ctx, "backup.staleness.start", slog.String("bucket", cfg.BackupS3BucketMain))

	// Each metric is dated against a clock read when its own probe runs, not
	// against one reading taken up front. The probes are serial and talk to an
	// object store, a cluster, and a container runtime, so a slow earlier probe
	// would otherwise push a later sample past the future-timestamp tolerance
	// and report a healthy mechanism as corrupt.
	metrics := []backupStalenessMetric{
		exportStalenessMetric(ctx, cfg, s3Client, opsNow().UTC()),
		markerStalenessMetric(ctx, cfg, s3Client, backupStalenessRehearsalName, opsNow().UTC(),
			backupStalenessThreshold(cfg.BackupStalenessRehearsalMaxSeconds)),
		replicationStalenessMetric(ctx, cfg, s3Client, opsNow().UTC()),
	}
	if cfg.BackupFDBContinuous {
		metrics = append(metrics, fdbStalenessMetric(ctx, cfg, opsNow().UTC()))
	} else {
		logger.WarnContext(ctx, "backup.staleness.metric_skipped",
			slog.String("metric", backupStalenessFDBName),
			slog.String("reason", "TACK_BACKUP_FDB_CONTINUOUS is false"))
	}

	// A report that cannot be written is noted and carried to the end: the
	// reading was taken, the log still records it, and the alarm below must
	// not depend on the output stream. The write failure is what the run
	// returns only when nothing is stale.
	var writeErr error
	if _, err := io.WriteString(out, backupStalenessReport(metrics)); err != nil {
		writeErr = fmt.Errorf("write staleness report: %w", err)
		logger.ErrorContext(ctx, "backup.staleness.failed", slog.String("err", writeErr.Error()))
	}
	logBackupStalenessMetrics(ctx, metrics)

	// The alarm runs on every reading, stale or not, because a mechanism that
	// has come back is what resets its memory. The mail is an attempt, never
	// a gate: whatever it does, the stale verdict is what this run returns.
	alarmBackupStalenessTransitions(ctx, cfg, metrics)

	stale := staleBackupStalenessMetrics(metrics)
	if len(stale) > 0 {
		err := fmt.Errorf("staleness-check: %d backup mechanism(s) past threshold: %s",
			len(stale), strings.Join(stale, ", "))
		logger.ErrorContext(ctx, "backup.staleness.stale", slog.String("err", err.Error()))
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	logger.InfoContext(ctx, "backup.staleness.ok", slog.Int("metric_count", len(metrics)))
	return nil
}

// logBackupStalenessMetrics records the whole reading in one summary event, so
// the verdicts survive in the log trail even when nobody reads the mail.
func logBackupStalenessMetrics(ctx context.Context, metrics []backupStalenessMetric) {
	readings := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		readings = append(readings, fmt.Sprintf("%s age=%s threshold=%s %s",
			metric.Name,
			backupStalenessAgeField(metric),
			backupStalenessSeconds(metric.Threshold),
			metric.verdict()))
	}
	telemetry.L(ctx).InfoContext(ctx, "backup.staleness.metrics", slog.Any("readings", readings))
}

// exportStalenessMetric dates the off-host ledger export from the newest
// complete export run's key, so the export needs no marker of its own: a run
// key is a UTC timestamp and the completeness walk already knows which run is
// restorable. A store that cannot be listed and a bucket with no complete run
// both report an unknown age, which is stale.
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
		return unknownBackupStalenessMetric(backupStalenessExportName, threshold,
			backupStalenessUnreadable, "listing export runs failed: "+err.Error())
	}
	manifest, found, err := newestCompleteYBSnapshotRun(runIDs,
		func(runID string) (ybSnapshotManifest, error) {
			return fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
		},
		func(key string) (bool, error) {
			return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
		func(runID, reason string) {
			logger.InfoContext(ctx, "backup.staleness.export_run_skipped",
				slog.String("run_id", runID), slog.String("reason", reason))
		})
	if err != nil {
		return unknownBackupStalenessMetric(backupStalenessExportName, threshold,
			backupStalenessUnreadable, "walking export runs failed: "+err.Error())
	}
	if !found {
		return unknownBackupStalenessMetric(backupStalenessExportName, threshold,
			backupStalenessNeverRecorded, "no complete export run in "+cfg.BackupS3BucketMain)
	}
	at, err := time.Parse(ybSnapshotRunIDLayout, manifest.RunID)
	if err != nil {
		return unknownBackupStalenessMetric(backupStalenessExportName, threshold,
			backupStalenessUnreadable, "export run key "+manifest.RunID+" is not a run-key timestamp")
	}
	return knownBackupStalenessMetric(ctx, backupStalenessExportName, now, at, threshold,
		"newest complete run "+manifest.RunID)
}

// markerStalenessMetric dates a mechanism from its success marker. An absent
// marker means the mechanism has never recorded a success, which is stale.
func markerStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	name string,
	now time.Time,
	threshold time.Duration,
) backupStalenessMetric {
	marker, found, err := readBackupStatusMarker(ctx,
		func(key string) ([]byte, error) {
			return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
		}, name)
	if err != nil {
		return unknownBackupStalenessMetric(name, threshold, backupStalenessUnreadable,
			"reading "+backupStatusKey(name)+" failed: "+err.Error())
	}
	if !found {
		return unknownBackupStalenessMetric(name, threshold, backupStalenessNeverRecorded,
			"no "+backupStatusKey(name)+" in "+cfg.BackupS3BucketMain)
	}
	return knownBackupStalenessMetric(ctx, name, now, marker.At, threshold, marker.Detail)
}

// replicationStalenessMetric probes the live cluster and records the moment it
// last looked healthy. Nothing else writes this marker, so the check both makes
// and reads the observation: a healthy answer refreshes the marker, and any
// other answer leaves the marker where it was, which ages into an alert if the
// cluster stays degraded. An unhealthy answer becomes the metric's detail: on
// its own when the marker dated the last healthy observation, and after the
// marker's own reason when nothing did, so the report and the mail keep both
// why the age is unknown and what this run saw.
func replicationStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	now time.Time,
) backupStalenessMetric {
	logger := telemetry.L(ctx)
	threshold := backupStalenessThreshold(cfg.BackupStalenessReplicationMaxSeconds)
	healthy, detail := probeYBClusterHealth(ctx, cfg)
	if healthy {
		// A failed write is logged by the marker writer and left to age: the
		// observation is real, but an unrecorded observation must not be
		// reported as a success.
		_ = writeBackupStatusMarker(ctx,
			func(key string, body []byte) error {
				return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
			}, backupStalenessReplicationName, now, detail)
	} else {
		logger.WarnContext(ctx, "backup.staleness.replication_unhealthy",
			slog.String("detail", detail))
	}
	metric := markerStalenessMetric(ctx, cfg, s3Client, backupStalenessReplicationName, now, threshold)
	if healthy {
		return metric
	}
	if metric.AgeKnown {
		metric.Detail = detail
		return metric
	}
	metric.Detail += "; this run observed: " + detail
	return metric
}

// fdbStalenessMetric dates the FoundationDB continuous backup from its
// restorable point. It runs only when continuous backup is enabled, so the
// docker client and the one-shot container are not built for a deployment that
// has no FoundationDB backup to measure.
func fdbStalenessMetric(ctx context.Context, cfg *config.Config, now time.Time) backupStalenessMetric {
	logger := telemetry.L(ctx)
	threshold := backupStalenessThreshold(cfg.BackupStalenessFDBMaxSeconds)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return unknownBackupStalenessMetric(backupStalenessFDBName, threshold,
			backupStalenessUnreadable, "docker client unavailable: "+redactSecret(cfg, err.Error()))
	}
	defer cli.Close()

	status, err := fdbBackupStatusText(ctx, &backupCtx{
		Cfg:     cfg,
		Cli:     cli,
		Log:     logger,
		RunID:   "",
		DestDir: "",
	})
	if err != nil {
		return unknownBackupStalenessMetric(backupStalenessFDBName, threshold,
			backupStalenessUnreadable, "fdbbackup status failed: "+redactSecret(cfg, err.Error()))
	}
	at, err := fdbRestorablePointFromStatus(ctx, status)
	if errors.Is(err, errFDBNoRestorablePoint) {
		return unknownBackupStalenessMetric(backupStalenessFDBName, threshold,
			backupStalenessNeverRecorded, redactSecret(cfg, err.Error()))
	}
	if err != nil {
		return unknownBackupStalenessMetric(backupStalenessFDBName, threshold,
			backupStalenessUnreadable, redactSecret(cfg, err.Error()))
	}
	return knownBackupStalenessMetric(ctx, backupStalenessFDBName, now, at, threshold,
		"restorable through "+at.UTC().Format(time.RFC3339))
}
