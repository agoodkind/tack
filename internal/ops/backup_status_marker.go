// Backup commands record the last observed success for each mechanism as a
// small JSON object under backup-status/. The staleness check reads these
// markers. A mechanism with an existing dated artifact, such as the ledger
// export's run key, does not need or write a separate marker.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// backupStatusPrefix specifies the object-store prefix for each backup
// mechanism's success marker.
const backupStatusPrefix = "backup-status/"

// backupStatusMarker records a mechanism's last observed success. Detail
// identifies the observation and the run or probe that wrote the marker.
type backupStatusMarker struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// backupStatusKey returns the object key for a mechanism's success marker.
func backupStatusKey(metric string) string {
	return backupStatusPrefix + metric + ".json"
}

// writeBackupStatusMarker records a success for metric using put. The caller
// connects put to the object store. The writer converts the timestamp to UTC
// for comparisons between hosts in different time zones.
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

// readBackupStatusMarker fetches and decodes a mechanism's marker using the
// caller's object-store get function. It returns found=false when the marker
// does not exist, indicating that the store does not provide a recorded
// success. It rejects a decoded marker without a timestamp rather than
// accepting a success at the zero time.
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
		wrapped := fmt.Errorf("backup status marker %s carries no at timestamp", key)
		logger.ErrorContext(ctx, "backup.status.marker_failed", slog.String("err", wrapped.Error()))

		return absent, false, wrapped
	}

	return marker, true, nil
}
