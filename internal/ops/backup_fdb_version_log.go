// FoundationDB version logs preserve observed version-time pairs in the
// object store for recovery after total source-cluster loss (TACK-468).
// Normal timestamp conversion requires a surviving source cluster. Recorded
// restorable points support choosing a version when that cluster is unavailable.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/telemetry"
)

const fdbVersionLogKey = backupStatusPrefix + "fdb-versions.json"

// fdbVersionLogMaxEntries bounds stored observations and encoded record size.
const fdbVersionLogMaxEntries = 6000

const (
	// fdbVersionLogMaxEntryBytes accounts for a 20-character int64, a
	// 30-character nanosecond UTC timestamp, field syntax, and a comma.
	fdbVersionLogMaxEntryBytes = 71
	// fdbVersionLogMarginBytes permits the JSON envelope and additional space.
	fdbVersionLogMarginBytes = 1024
	// fdbVersionLogMaxBytes accepts a full maximum-width record while bounding
	// in-memory reads of corrupt or hostile oversized objects.
	fdbVersionLogMaxBytes = fdbVersionLogMaxEntries*fdbVersionLogMaxEntryBytes + fdbVersionLogMarginBytes
)

type fdbRestorablePoint struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

// fdbVersionLog stores observations oldest first.
type fdbVersionLog struct {
	Points []fdbRestorablePoint `json:"points"`
}

// fdbVersionLogGetter supplies the version-log-specific memory limit for both
// reads and append operations.
func fdbVersionLogGetter(
	ctx context.Context,
	client *s3.Client,
	bucket string,
) func(key string) ([]byte, error) {
	return func(key string) ([]byte, error) {
		return getObjectBytesUpTo(ctx, client, bucket, key, fdbVersionLogMaxBytes)
	}
}

// readFDBVersionLog decodes the record and treats absence as empty initial
// state.
func readFDBVersionLog(
	ctx context.Context,
	get func(key string) ([]byte, error),
) (fdbVersionLog, error) {
	var log fdbVersionLog
	body, err := get(fdbVersionLogKey)
	if err != nil {
		if isObjectNotFound(err) {
			return log, nil
		}

		return log, err
	}

	if err := json.Unmarshal(body, &log); err != nil {
		wrapped := fmt.Errorf("unmarshal %s: %w", fdbVersionLogKey, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.fdb_versions.unreadable",
			slog.String("err", wrapped.Error()),
		)

		return fdbVersionLog{}, wrapped
	}

	return log, nil
}

// appendFDBRestorablePoint sorts observations by version, ignores duplicate
// versions without replacing their original timestamps, and bounds retained
// entries. Repeated status checks must not grow an idle backup's record.
func appendFDBRestorablePoint(
	ctx context.Context,
	get func(key string) ([]byte, error),
	put func(key string, body []byte) error,
	point fdbRestorablePoint,
) error {
	logger := telemetry.L(ctx)
	log, err := readFDBVersionLog(ctx, get)
	if err != nil {
		return err
	}

	index, found := slices.BinarySearchFunc(
		log.Points,
		point,
		func(a, b fdbRestorablePoint) int {
			switch {
			case a.Version < b.Version:
				return -1
			case a.Version > b.Version:
				return 1
			default:
				return 0
			}
		},
	)
	if found {
		return nil
	}

	point.At = point.At.UTC()
	log.Points = slices.Insert(log.Points, index, point)
	if len(log.Points) > fdbVersionLogMaxEntries {
		log.Points = log.Points[len(log.Points)-fdbVersionLogMaxEntries:]
	}

	body, err := json.Marshal(log)
	if err != nil {
		wrapped := fmt.Errorf("marshal %s: %w", fdbVersionLogKey, err)
		logger.ErrorContext(
			ctx,
			"backup.fdb_versions.unwritable",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if err := put(fdbVersionLogKey, body); err != nil {
		return err
	}

	logger.InfoContext(
		ctx,
		"backup.fdb_versions.recorded",
		slog.Int64("version", point.Version),
		slog.Time("at", point.At),
		slog.Int("points", len(log.Points)),
	)

	return nil
}

// fdbVersionAt selects the newest observation at or before want. This version
// restores mutations completed by that observation, not the exact requested
// instant. found=false indicates a time before the retained record, not
// necessarily before the backup began.
func fdbVersionAt(log fdbVersionLog, want time.Time) (point fdbRestorablePoint, found bool) {
	for _, candidate := range slices.Backward(log.Points) {
		if !candidate.At.After(want) {
			return candidate, true
		}
	}

	return fdbRestorablePoint{Version: 0, At: time.Time{}}, false
}
