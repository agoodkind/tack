// backup_fdb_version_log.go keeps the record that turns a wall-clock moment
// into a FoundationDB version after the source cluster is gone.
//
// Restoring to a moment normally asks the surviving cluster to convert the
// time, so the failure the feature is most wanted for, a total loss, is the one
// where it does not work; a restore then has to name a version number read out
// of the backup itself, with nothing relating those numbers to when anything
// happened (TACK-468). The staleness check already reads the restorable point
// every few minutes, so each reading's version and timestamp are appended here,
// in the object store, which survives the cluster.

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

// fdbVersionLogKey is the object holding the version-to-time record.
const fdbVersionLogKey = backupStatusPrefix + "fdb-versions.json"

// fdbVersionLogMaxEntries bounds the record. The staleness check reads every
// ten minutes, so this holds about six weeks, well past any restorable window
// the continuous backup keeps, and the whole record stays a few hundred
// kilobytes.
const fdbVersionLogMaxEntries = 6000

const (
	// fdbVersionLogMaxEntryBytes is the widest one entry encodes to, its
	// separating comma included: a 20-character version (the most negative
	// int64) and a 30-character UTC timestamp with nanoseconds, in
	// {"version":V,"at":"T"},
	fdbVersionLogMaxEntryBytes = 71
	// fdbVersionLogMarginBytes covers the {"points":[]} envelope with room to
	// spare.
	fdbVersionLogMarginBytes = 1024
	// fdbVersionLogMaxBytes is the in-memory read limit for the record: a full
	// record of the widest entries fits, while an object far past any record
	// the writer produces is still refused unread.
	fdbVersionLogMaxBytes = fdbVersionLogMaxEntries*fdbVersionLogMaxEntryBytes + fdbVersionLogMarginBytes
)

// fdbRestorablePoint is one reading of the continuous backup's restorable
// point: the version a restore would name and the moment it corresponds to.
type fdbRestorablePoint struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

// fdbVersionLog is the stored record, oldest entry first.
type fdbVersionLog struct {
	Points []fdbRestorablePoint `json:"points"`
}

// fdbVersionLogGetter reads objects from bucket up to fdbVersionLogMaxBytes,
// the getter every reader and writer of the record passes.
func fdbVersionLogGetter(ctx context.Context, client *s3.Client, bucket string) func(key string) ([]byte, error) {
	return func(key string) ([]byte, error) {
		return getObjectBytesUpTo(ctx, client, bucket, key, fdbVersionLogMaxBytes)
	}
}

// readFDBVersionLog fetches the record through get. An absent record is an
// empty one, because the first reading has to start somewhere.
func readFDBVersionLog(ctx context.Context, get func(key string) ([]byte, error)) (fdbVersionLog, error) {
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
		telemetry.L(ctx).ErrorContext(ctx, "backup.fdb_versions.unreadable", slog.String("err", wrapped.Error()))
		return fdbVersionLog{}, wrapped
	}
	return log, nil
}

// appendFDBRestorablePoint records one reading, keeping the record sorted by
// version, free of duplicates, and bounded. A reading whose version is already
// recorded changes nothing, which is the ordinary case between two checks that
// see the same restorable point.
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
	index, found := slices.BinarySearchFunc(log.Points, point, func(a, b fdbRestorablePoint) int {
		switch {
		case a.Version < b.Version:
			return -1
		case a.Version > b.Version:
			return 1
		default:
			return 0
		}
	})
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
		logger.ErrorContext(ctx, "backup.fdb_versions.unwritable", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := put(fdbVersionLogKey, body); err != nil {
		return err
	}
	logger.InfoContext(ctx, "backup.fdb_versions.recorded",
		slog.Int64("version", point.Version),
		slog.Time("at", point.At),
		slog.Int("points", len(log.Points)),
	)
	return nil
}

// fdbVersionAt returns the newest recorded point at or before want, which is
// the version a restore to that moment names. It is not the exact moment: a
// restore to the returned version reaches every mutation the backup had drained
// by that reading, so the answer is the last reading that does not overshoot.
// found is false when the record holds nothing that old, which means the
// moment predates the record rather than predating the backup.
func fdbVersionAt(log fdbVersionLog, want time.Time) (point fdbRestorablePoint, found bool) {
	for _, candidate := range slices.Backward(log.Points) {
		if !candidate.At.After(want) {
			return candidate, true
		}
	}
	return fdbRestorablePoint{Version: 0, At: time.Time{}}, false
}
