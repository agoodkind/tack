// The version record converts a wall-clock target to a FoundationDB version
// after the source cluster has been lost.
//
// A wall-clock restore normally asks the surviving cluster to convert time
// to a version. After a total loss, recovery must instead use a version from
// the backup, which does not by itself establish the time of that version
// (TACK-468). The staleness check already reads the restorable point every few
// minutes. It appends each reading's version and timestamp to the object
// store record, which remains available after cluster loss.

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

// fdbVersionLogKey identifies the object that stores the version-to-time
// record.
const fdbVersionLogKey = backupStatusPrefix + "fdb-versions.json"

// fdbVersionLogMaxEntries limits the record's entry count. The original
// sizing estimate assumed ten-minute staleness checks: about six weeks of
// readings, beyond the continuous backup's restorable window, in a few
// hundred kilobytes.
const fdbVersionLogMaxEntries = 6000

const (
	// fdbVersionLogMaxEntryBytes includes an entry's separating comma. The widest
	// entry uses a 20-character version (the most negative int64) and a
	// 30-character UTC timestamp with nanoseconds. The form
	// '{"version":V,"at":"T"},'
	// includes that comma.
	fdbVersionLogMaxEntryBytes = 71
	// fdbVersionLogMarginBytes provides space for the {"points":[]} envelope
	// and extra capacity.
	fdbVersionLogMarginBytes = 1024
	// fdbVersionLogMaxBytes limits record reads in memory. It permits a full
	// record of the widest entries and refuses objects far larger than the
	// writer's output before reading their complete contents.
	fdbVersionLogMaxBytes = fdbVersionLogMaxEntries*fdbVersionLogMaxEntryBytes + fdbVersionLogMarginBytes
)

// fdbRestorablePoint records one continuous-backup reading with its restore
// version and corresponding time.
type fdbRestorablePoint struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

// fdbVersionLog stores the record with the oldest entry first.
type fdbVersionLog struct {
	Points []fdbRestorablePoint `json:"points"`
}

// fdbVersionLogGetter reads bucket objects up to fdbVersionLogMaxBytes.
// Every reader and writer of the record uses this getter.
func fdbVersionLogGetter(
	ctx context.Context,
	client *s3.Client,
	bucket string,
) func(key string) ([]byte, error) {
	return func(key string) ([]byte, error) {
		return getObjectBytesUpTo(ctx, client, bucket, key, fdbVersionLogMaxBytes)
	}
}

// readFDBVersionLog fetches the record through get. It treats a missing
// record as empty to permit the first reading.
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

// appendFDBRestorablePoint records one reading, sorts entries by version,
// avoids duplicate versions, and limits the entry count. It does not change an
// already-recorded version, as when consecutive checks observe the same
// restorable point.
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

// fdbVersionAt returns the newest recorded point at or before want. A restore
// to its version includes every mutation that the backup had copied by that
// reading. The point can precede the exact target moment and does not exceed
// it.
//
// found is false when the record does not contain an old enough reading.
// That result establishes the record's limit, rather than the backup's age.
func fdbVersionAt(log fdbVersionLog, want time.Time) (point fdbRestorablePoint, found bool) {
	for _, candidate := range slices.Backward(log.Points) {
		if !candidate.At.After(want) {
			return candidate, true
		}
	}

	return fdbRestorablePoint{Version: 0, At: time.Time{}}, false
}
