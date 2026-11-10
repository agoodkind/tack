// The check records each mechanism's last readable success and read time on
// this guest (TACK-515). A failed current read can use that success to date
// the mechanism. During a brief store outage, the remembered success
// remains fresh until its age exceeds the threshold. After that threshold,
// the mechanism may have become stale without a usable current reading.
// A mechanism without a previous readable success becomes stale at once
// when its current reading fails.
//
// The record uses a local file under the backup root. Shared alarm state
// uses the object store and cannot supply this local history when that
// store fails to answer.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupLastReadingFile identifies the file under the backup root that
// stores each mechanism's last readable success.
const backupLastReadingFile = "staleness-last-reading.json"

// backupLastReading records a mechanism's last readable success and the
// time this guest read it.
type backupLastReading struct {
	SucceededAt time.Time `json:"succeeded_at"`
	ReadAt      time.Time `json:"read_at"`
}

// backupLastReadings is the whole record, keyed by metric name.
type backupLastReadings struct {
	Readings map[string]backupLastReading `json:"readings"`
}

// lastBackupStalenessSuccess returns the last success that this guest read
// for a mechanism. The replication probe uses it directly to date this
// guest's last healthy cluster observation instead of using the shared
// success marker.
func lastBackupStalenessSuccess(ctx context.Context, cfg *config.Config, name string) (time.Time, bool) {
	last, found := loadBackupLastReadings(ctx, cfg).Readings[name]
	if !found || last.SucceededAt.IsZero() {
		return time.Time{}, false
	}
	return last.SucceededAt.UTC(), true
}

// rememberBackupStalenessReadings saves successful readings from this run
// and uses the record to date failed current readings. A confirmed absence
// of success removes the stored reading; a later outage must not reuse that
// denied evidence. A failed reading with a known age already uses the
// probe's chosen date, and this function preserves it.
func rememberBackupStalenessReadings(
	ctx context.Context,
	cfg *config.Config,
	metrics []backupStalenessMetric,
	now time.Time,
) []backupStalenessMetric {
	record := loadBackupLastReadings(ctx, cfg)
	remembered := make([]backupStalenessMetric, 0, len(metrics))
	for _, metric := range metrics {
		last, found := record.Readings[metric.Name]
		switch {
		case metric.Unknown == backupStalenessAgeKnown:
			record.Readings[metric.Name] = backupLastReading{SucceededAt: metric.At, ReadAt: now}
		case metric.Unknown == backupStalenessNeverRecorded:
			delete(record.Readings, metric.Name)
		case found && !metric.AgeKnown:
			metric = rememberedBackupStalenessMetric(ctx, metric, last, now)
		}
		remembered = append(remembered, metric)
	}
	saveBackupLastReadings(ctx, cfg, record)
	return remembered
}

// rememberedBackupStalenessMetric dates an unreadable metric from its last
// readable success. It preserves the failure cause for the report and mail,
// and preserves failure details for the journal.
func rememberedBackupStalenessMetric(
	ctx context.Context,
	metric backupStalenessMetric,
	last backupLastReading,
	now time.Time,
) backupStalenessMetric {
	metric.At = last.SucceededAt.UTC()
	metric.Age = backupStalenessAge(now, last.SucceededAt)
	metric.AgeKnown = true
	metric.LastReadAt = last.ReadAt.UTC()
	metric.Detail = "unreadable, dated from the reading at " +
		last.ReadAt.UTC().Format(time.RFC3339) + ": " + metric.Detail
	telemetry.L(ctx).InfoContext(ctx, "backup.staleness.reading_remembered",
		slog.String("metric", metric.Name),
		slog.Time("succeeded_at", metric.At),
		slog.Time("read_at", metric.LastReadAt),
		slog.Bool("stale", metric.stale()))
	return metric
}

// backupLastReadingPath returns the record path for cfg.
func backupLastReadingPath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupLastReadingFile)
}

// loadBackupLastReadings reads the local record. An absent file supplies
// an empty record. An unreadable or damaged file also supplies an empty
// record and logs the failure. Without a remembered success, an unreadable
// current reading remains stale.
func loadBackupLastReadings(ctx context.Context, cfg *config.Config) backupLastReadings {
	empty := backupLastReadings{Readings: map[string]backupLastReading{}}
	path := backupLastReadingPath(cfg)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty
	}
	if err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.last_reading_unreadable",
			slog.String("path", path), slog.String("err", err.Error()))
		return empty
	}
	var record backupLastReadings
	if err := json.Unmarshal(body, &record); err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.last_reading_unreadable",
			slog.String("path", path), slog.String("err", err.Error()))
		return empty
	}
	if record.Readings == nil {
		record.Readings = map[string]backupLastReading{}
	}
	return record
}

// saveBackupLastReadings replaces the record with a rename, matching alarm
// state writes. It logs write failures and lets the check continue. A later
// outage must then use an older saved success or an unknown age, which can
// alarm sooner.
func saveBackupLastReadings(ctx context.Context, cfg *config.Config, record backupLastReadings) {
	logger := telemetry.L(ctx)
	path := backupLastReadingPath(cfg)
	body, err := json.Marshal(record)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.last_reading_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	if err := os.MkdirAll(cfg.BackupRoot, 0o750); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.last_reading_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	partial, err := stageBackupAlarmState(ctx, path, body)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.last_reading_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	if err := os.Rename(partial, path); err != nil {
		// Remove the temporary file that this invocation created.
		_ = os.Remove(partial)
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(ctx, "backup.staleness.last_reading_write_failed",
			slog.String("path", path), slog.String("err", renameErr.Error()))
		return
	}
	logger.DebugContext(ctx, "backup.staleness.last_reading_written",
		slog.String("path", path), slog.Int("reading_count", len(record.Readings)))
}
