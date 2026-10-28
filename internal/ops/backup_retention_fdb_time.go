package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// retentionFDBRetainedVersion uses the native timestamp conversion and verifies
// restorability without configuring a destination database or submitting a restore.
func retentionFDBRetainedVersion(ctx context.Context, cfg *config.Config, destination string, cutoff time.Time) (int64, error) {
	timestamp, err := fdbRestoreTimestampArg(cutoff.UTC())
	if err != nil {
		return 0, err
	}
	output, err := retentionFDBToolCommand(ctx, cfg, "fdbrestore", "start", []string{
		"--dryrun", "--timestamp", timestamp,
		"--orig-cluster-file", retentionFDBClusterFile, "-r", destination,
	})
	if err != nil {
		return 0, err
	}
	prefix := "Timestamp '" + timestamp + "' resolves to version "
	var resolved int64
	for line := range strings.SplitSeq(output, "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !found {
			continue
		}
		if resolved != 0 {
			return 0, errors.New("FoundationDB timestamp conversion reported multiple versions")
		}
		version, err := strconv.ParseInt(value, 10, 64)
		if err != nil || version <= 0 {
			return 0, errors.New("FoundationDB timestamp conversion reported an invalid version")
		}
		resolved = version
	}
	if resolved == 0 {
		return 0, errors.New("FoundationDB timestamp conversion did not report the retained version")
	}
	return resolved, nil
}

// fdbStatusTimestampPrecision is the resolution of the moment fdbbackup status
// prints beside a version. The status truncates that moment to the second.
const fdbStatusTimestampPrecision = time.Second

// retentionFDBCutoffVersion converts the cutoff to a version from the version
// log (TACK-468), which the object store keeps and which needs no cluster. The
// plan compares every restorable window against this one version.
//
// The log version is safe because it is at or before the true cutoff version.
// Each reading pairs a version with its moment truncated to the second. The
// cluster committed the version of a reading stamped at least one second
// before the cutoff before the cutoff itself. A version at or below the true
// cutoff version can keep a backup that the true version would expire or
// delete, and it cannot select a backup that the true version would keep. The
// native guards still check the exact cutoff timestamp: the `fdbrestore
// --dryrun --timestamp` conversion before each expiration and survivor, and
// `--restorable-after-timestamp` on the expiration itself.
func retentionFDBCutoffVersion(ctx context.Context, cfg *config.Config, cutoff time.Time) (int64, error) {
	logger := telemetry.L(ctx)
	versionLog, err := readFDBVersionLog(ctx, fdbVersionLogGetter(ctx, newBackupS3Client(cfg), cfg.BackupS3BucketMain))
	if err != nil {
		wrapped := fmt.Errorf("read FoundationDB version log %s for cutoff %s: %w", fdbVersionLogKey, formatFDBTime(cutoff), err)
		logger.ErrorContext(ctx, "backup.retention.fdb.cutoff_version_failed", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	point, found := fdbVersionAt(versionLog, cutoff.UTC().Add(-fdbStatusTimestampPrecision))
	if !found {
		err := fmt.Errorf("FoundationDB version log %s does not have a reading before cutoff %s",
			fdbVersionLogKey, formatFDBTime(cutoff))
		logger.ErrorContext(ctx, "backup.retention.fdb.cutoff_version_failed", slog.String("err", err.Error()))
		return 0, err
	}
	logger.InfoContext(ctx, "backup.retention.fdb.cutoff_version_read",
		slog.Int64("version", point.Version),
		slog.Time("reading_at", point.At),
		slog.Time("cutoff", cutoff),
	)
	return point.Version, nil
}
