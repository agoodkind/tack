// Commands record backup successes as JSON markers for the staleness check.
// Mechanisms with timestamped artifacts, such as ledger export run keys, do not
// need separate markers.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// backupStatusPrefix is the object-store folder for backup success markers.
const backupStatusPrefix = "backup-status/"

// backupStatusMarker records a mechanism's last observed success.
// Detail identifies the run or probe that observed it.
type backupStatusMarker struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// backupStatusKey is the object key of one mechanism's success marker.
func backupStatusKey(metric string) string {
	return backupStatusPrefix + metric + ".json"
}

// writeBackupStatusMarker records a success through the caller's
// object-store put function. It stores the timestamp in UTC for comparisons
// across time zones.
func writeBackupStatusMarker(
	ctx context.Context,
	put func(key string, body []byte) error,
	metric string,
	at time.Time,
	detail string,
) error {
	logger := telemetry.L(ctx)
	key := backupStatusKey(metric)
	body, err := json.Marshal(backupStatusMarker{
		At:     at.UTC(),
		Detail: detail,
	})
	if err != nil {
		wrapped := fmt.Errorf("marshal backup status marker %s: %w", key, err)
		logger.ErrorContext(ctx, "backup.status.marker_failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	if err := put(key, body); err != nil {
		return err
	}

	logger.InfoContext(
		ctx,
		"backup.status.marker_written",
		slog.String("metric", metric),
		slog.String("key", key),
		slog.Time("at", at.UTC()),
		slog.String("detail", detail),
	)

	return nil
}

// readBackupStatusMarker decodes a marker from the caller's object-store get
// function. It returns found=false for an absent marker and rejects a zero
// timestamp rather than reporting success at the zero time.
func readBackupStatusMarker(
	ctx context.Context,
	get func(key string) ([]byte, error),
	metric string,
) (marker backupStatusMarker, found bool, err error) {
	logger := telemetry.L(ctx)
	var absent backupStatusMarker
	key := backupStatusKey(metric)
	body, err := get(key)
	if err != nil {
		if isObjectNotFound(err) {
			return absent, false, nil
		}

		return absent, false, err
	}

	if err := json.Unmarshal(body, &marker); err != nil {
		wrapped := fmt.Errorf("unmarshal backup status marker %s: %w", key, err)
		logger.ErrorContext(ctx, "backup.status.marker_failed", slog.String("err", wrapped.Error()))

		return absent, false, wrapped
	}

	if marker.At.IsZero() {
		wrapped := backupMarkerTimestampMissingError{Key: key}
		logger.ErrorContext(ctx, "backup.status.marker_failed", slog.String("err", wrapped.Error()))

		return absent, false, wrapped
	}

	return marker, true, nil
}

type backupMarkerTimestampMissingError struct {
	Key string
}

func (failure backupMarkerTimestampMissingError) Error() string {
	return fmt.Sprintf(
		"backup status marker %s does not contain a nonzero at timestamp",
		failure.Key,
	)
}
