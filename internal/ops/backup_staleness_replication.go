// backup_staleness_replication.go is the ledger cluster's freshness reading:
// how long ago the guest running the check last saw the cluster healthy
// itself. The reading is dated from this guest's own last observation, held in
// the last-reading record, and never from the success marker in the object
// store, because that marker is shared: any guest that observes the cluster
// healthy refreshes it, so an owner guest blocked from every master reads the
// deputy's observation as its own and stays silent while it is blind
// (TACK-529). The owner guest is the one that takes the backups, so its
// blindness is the one that must be heard.
//
// The marker is still written on every healthy observation. It is the record
// of when the cluster was last seen healthy from anywhere, which is the
// question a per-guest reading cannot answer, and a blind guest's report says
// what it holds.

package ops

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybClusterObservation is what this guest's own probe established about the
// ledger cluster. The staleness check keys the alarm's words on it, because a
// guest that reached no master has established nothing and must not say the
// cluster is unhealthy.
type ybClusterObservation int

const (
	// ybClusterHealthy means a master answered and vouched for every node
	// alive and every tablet fully replicated.
	ybClusterHealthy ybClusterObservation = iota
	// ybClusterDegraded means a master answered and the cluster it described
	// is not fully replicated.
	ybClusterDegraded
	// ybClusterUnseen means no master gave this guest a usable answer.
	ybClusterUnseen
)

// replicationStalenessMetric probes the live cluster and dates the reading from
// the last time this guest saw the cluster healthy.
//
// A healthy answer is that observation: the reading ages from now, and the
// shared marker records it for whoever reads the store. Any other answer
// leaves the age counting from the earlier observation, so a guest that keeps
// failing to see the cluster passes its threshold and alarms, whatever another
// guest can see.
//
// The cause separates the two ways an answer can fail. A master that answered
// and described a cluster short of its replicas is a degraded cluster, which
// the mail says outright; a guest that reached no master has established
// nothing, and its mail says only that this guest cannot see the cluster.
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
		// A failed write is logged by the marker writer and left alone: the
		// observation is this guest's own and dates the reading either way.
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

// unseenClusterStalenessMetric is the reading of a guest that reached no
// master. The cause marks it as unseen whether or not an earlier observation
// dates it, so the mail claims only this guest's blindness, and the report
// carries what the shared marker says beside it.
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

// withSharedClusterRecord adds what the shared marker holds to a blind guest's
// reading, so the journal separates a cluster nobody can see from one only
// this guest cannot see. The marker never dates the reading, and a marker that
// is absent or unreadable simply adds nothing.
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
