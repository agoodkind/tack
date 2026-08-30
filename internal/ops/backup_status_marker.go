// backup_status_marker.go holds the success markers the staleness check reads:
// one small JSON object per backup mechanism under backup-status/, written by
// whichever command observed that mechanism succeed. A mechanism whose last
// success is already datable from an artifact it publishes anyway, such as the
// ledger export's run key, needs no marker and gets none.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// backupStatusPrefix is the object-store folder that holds one success marker
// per backup mechanism.
const backupStatusPrefix = "backup-status/"

// backupStatusMarker records one mechanism's last observed success. Detail
// names what was observed so an operator reading the marker can tell which run
// or probe wrote it.
type backupStatusMarker struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// backupStatusKey is the object key of one mechanism's success marker.
func backupStatusKey(metric string) string {
	return backupStatusPrefix + metric + ".json"
}

// writeBackupStatusMarker records a success for metric through put, which the
// caller binds to the object store. The timestamp is stored in UTC so markers
// written by hosts in different zones compare directly.
func writeBackupStatusMarker(
	ctx context.Context,
	put func(key string, body []byte) error,
	metric string,
	at time.Time,
	detail string,
) error {
	logger := telemetry.L(ctx)
	key := backupStatusKey(metric)
	body, err := json.Marshal(backupStatusMarker{At: at.UTC(), Detail: detail})
	if err != nil {
		wrapped := fmt.Errorf("marshal backup status marker %s: %w", key, err)
		logger.ErrorContext(ctx, "backup.status.marker_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := put(key, body); err != nil {
		return err
	}
	logger.InfoContext(ctx, "backup.status.marker_written",
		slog.String("metric", metric),
		slog.String("key", key),
		slog.Time("at", at.UTC()),
		slog.String("detail", detail),
	)
	return nil
}

// readBackupStatusMarker fetches and decodes one mechanism's marker through
// get, which the caller binds to the object store. found is false when the
// marker is absent, which means the mechanism has never recorded a success.
// A marker that decodes but carries no timestamp is an error rather than a
// success: it would otherwise read as a success at the zero time.
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
