// Replication freshness uses the checking guest's own last healthy ledger
// cluster observation from its local record. It does not date that reading
// from the shared success marker. In TACK-529, an owner guest blocked from
// every master used the deputy's shared observation and failed to alarm.
// The owner takes the backups, and its failed observation requires an
// alarm even when another guest can observe the cluster.
//
// Healthy observations still attempt to refresh the shared marker. That
// marker records a healthy observation from a guest, rather than only this
// guest's history. A guest without a usable master answer includes the
// shared timestamp in its report when it can read the marker.

package ops

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybClusterObservation records what this guest's probe established about
// the ledger cluster. The alarm uses this value to distinguish observed
// degradation from an unusable master response. A failed observation must
// not assert that the cluster is unhealthy.
type ybClusterObservation int

const (
	// ybClusterHealthy means a master reported every node alive and every
	// tablet fully replicated.
	ybClusterHealthy ybClusterObservation = iota
	// ybClusterDegraded means a master reported dead nodes or under-replicated
	// tablets.
	ybClusterDegraded
	// ybClusterUnseen means this guest did not receive a usable master answer.
	ybClusterUnseen
)

// replicationStalenessMetric probes the live cluster and dates the reading
// from this guest's last healthy observation.
//
// A healthy answer dates the reading from now and attempts to update the
// shared marker. Other answers use the earlier local observation. Continued
// observation failure eventually exceeds the threshold even if another
// guest can observe the cluster.
//
// An answering master can report dead nodes or missing replicas, and the
// mail reports cluster degradation. An unusable master response cannot
// establish cluster health, and its mail reports only this guest's failed
// observation.
func replicationStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	now time.Time,
) backupStalenessMetric {
	logger := telemetry.L(ctx)
	name := backupStalenessReplicationName
	threshold := backupStalenessThreshold(cfg.BackupStalenessReplicationMaxSeconds)
	observation, detail := probeYBClusterHealth(ctx, cfg)
	if observation == ybClusterHealthy {
		// Preserve this guest's observed reading if the shared marker write fails.
		// The marker writer logs the failure.
		_ = writeBackupStatusMarker(ctx,
			func(key string, body []byte) error {
				return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
			}, name, now, detail)
		return knownBackupStalenessMetric(ctx, name, now, now, threshold, detail)
	}
	seenAt, seen := lastBackupStalenessSuccess(ctx, cfg, name)
	if observation == ybClusterUnseen {
		logger.WarnContext(ctx, "backup.staleness.replication_unseen", slog.String("detail", detail))
		return unseenClusterStalenessMetric(ctx, cfg, s3Client, now, threshold,
			detail, seenAt, seen)
	}
	logger.WarnContext(ctx, "backup.staleness.replication_unhealthy", slog.String("detail", detail))
	if !seen {
		return unknownBackupStalenessMetric(name, threshold, backupStalenessNeverRecorded, detail)
	}
	return knownBackupStalenessMetric(ctx, name, now, seenAt, threshold, detail)
}

// unseenClusterStalenessMetric builds a reading without a usable master
// answer. Its cause records failed observation even when an earlier local
// success supplies a known age. The mail reports only this guest's failed
// observation, and the report includes available shared-marker evidence.
func unseenClusterStalenessMetric(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	now time.Time,
	threshold time.Duration,
	detail string,
	seenAt time.Time,
	seen bool,
) backupStalenessMetric {
	name := backupStalenessReplicationName
	detail = withSharedClusterRecord(ctx, cfg, s3Client, detail)
	if !seen {
		return unknownBackupStalenessMetric(name, threshold, backupStalenessClusterUnseen, detail)
	}
	metric := knownBackupStalenessMetric(ctx, name, now, seenAt, threshold, detail)
	if metric.AgeKnown {
		metric.Unknown = backupStalenessClusterUnseen
	}
	return metric
}

// withSharedClusterRecord adds a readable shared marker's timestamp to the
// guest's detail. The journal can compare the shared healthy observation
// with this guest's failed probe. The marker does not date this guest's
// reading. An absent or unreadable marker does not change detail.
func withSharedClusterRecord(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	detail string,
) string {
	marker, found, err := readBackupStatusMarker(ctx,
		func(key string) ([]byte, error) {
			return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
		}, backupStalenessReplicationName)
	if err != nil || !found {
		return detail
	}
	return detail + "; the shared record dates the cluster healthy at " +
		marker.At.UTC().Format(time.RFC3339)
}
