// Replication freshness uses this guest's own last healthy observation, not a
// shared marker that another guest can refresh. Otherwise a guest unable to
// query any master could suppress its own connectivity alarm (TACK-529).
// Healthy observations still update the shared marker for cross-guest
// diagnostics, but that record never determines this guest's reading age.

package ops

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybClusterObservation separates a usable health answer from failed probes.
// Only a master's usable answer can establish degraded cluster replication.
type ybClusterObservation int

const (
	// ybClusterHealthy indicates an answer reporting all nodes alive and
	// every tablet fully replicated.
	ybClusterHealthy ybClusterObservation = iota
	// ybClusterDegraded indicates an answer reporting incomplete replication.
	ybClusterDegraded
	// ybClusterUnseen indicates that every master failed to provide a usable answer.
	ybClusterUnseen
)

// replicationStalenessMetric probes current health and dates failures from the
// previous local healthy observation. A healthy result records a shared success
// and resets local age. Degraded answers report cluster health; unanswered
// probes report only this guest's inability to establish health.
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
		// A failed shared write does not invalidate this guest's observation.
		// The writer logs its own failure.
		_ = writeBackupStatusMarker(
			ctx,
			func(key string, body []byte) error {
				return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
			},
			name,
			now,
			detail,
		)

		return knownBackupStalenessMetric(ctx, name, now, now, threshold, detail)
	}

	seenAt, seen := lastBackupStalenessSuccess(ctx, cfg, name)
	if observation == ybClusterUnseen {
		logger.WarnContext(
			ctx,
			"backup.staleness.replication_unseen",
			slog.String("detail", detail),
		)

		return unseenClusterStalenessMetric(
			ctx,
			cfg,
			s3Client,
			now,
			threshold,
			detail,
			seenAt,
			seen,
		)
	}

	logger.WarnContext(ctx, "backup.staleness.replication_unhealthy", slog.String("detail", detail))
	if !seen {
		return unknownBackupStalenessMetric(name, threshold, backupStalenessNeverRecorded, detail)
	}

	return knownBackupStalenessMetric(ctx, name, now, seenAt, threshold, detail)
}

// unseenClusterStalenessMetric retains the unseen cause even when a historical
// local observation supplies age. Shared marker detail adds diagnostic context
// without establishing current cluster health.
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

// withSharedClusterRecord adds a readable shared observation to report detail
// without dating this guest's metric. Missing or unreadable markers leave the
// original detail unchanged.
func withSharedClusterRecord(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	detail string,
) string {
	marker, found, err := readBackupStatusMarker(
		ctx,
		func(key string) ([]byte, error) {
			return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
		backupStalenessReplicationName,
	)
	if err != nil || !found {
		return detail
	}

	return detail + "; the shared record dates the cluster healthy at " +
		marker.At.UTC().Format(time.RFC3339)
}
