// backup_staleness_last_reading.go remembers, on this guest, each mechanism's
// newest success the check could read and when it read it, so a reading that
// cannot be taken is dated from the last one that was (TACK-515). A store
// that stops answering for a few minutes then leaves every mechanism fresh
// until its threshold, counted from the last success this guest saw, has
// passed; only then could the mechanism have gone stale unseen, and only then
// does the unreadable reading count as stale. A mechanism this guest never
// read has nothing to date it, so its unreadable reading stays stale at once.
// The record is a file under the backup root rather than part of the shared
// alarm memory, because the shared copy lives in the object store, which is
// exactly what cannot be read during the outages this record covers.

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

// backupLastReadingFile is the file under the backup root that holds the last
// readable success of each mechanism.
const backupLastReadingFile = "staleness-last-reading.json"

// backupLastReading is one mechanism's newest readable success: when the
// mechanism last succeeded and when this guest read that.
type backupLastReading struct {
	SucceededAt time.Time `json:"succeeded_at"`
	ReadAt      time.Time `json:"read_at"`
}

// backupLastReadings is the whole record, keyed by metric name.
type backupLastReadings struct {
	Readings map[string]backupLastReading `json:"readings"`
}

// lastBackupStalenessSuccess is the newest success this guest could read for
// one mechanism. The ledger cluster's leg reads it directly, because that
// mechanism's age is this guest's own last healthy observation rather than
// anything the shared store holds (backup_staleness_replication.go).
func lastBackupStalenessSuccess(ctx context.Context, cfg *config.Config, name string) (time.Time, bool) {
	last, found := loadBackupLastReadings(ctx, cfg).Readings[name]
	if !found || last.SucceededAt.IsZero() {
		return time.Time{}, false
	}
	return last.SucceededAt.UTC(), true
}

// rememberBackupStalenessReadings records every reading taken this run and
// dates every reading that could not be taken from the record. A reading that
// proves no success exists removes the record, so a later outage is not dated
// from a success the store has since denied. A reading that already carries an
// age was dated from this record by the leg that took it, so it is left alone.
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
// readable success. The cause stays, so the report and the mail still say the
// reading could not be taken; the detail keeps the failure for the journal.
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

// backupLastReadingPath is where the record lives for this configuration.
func backupLastReadingPath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupLastReadingFile)
}

// loadBackupLastReadings reads the record. An absent file is a guest that has
// read nothing yet; a damaged one is logged and read as empty, which dates
// nothing and so leaves every unreadable reading stale.
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

// saveBackupLastReadings replaces the record through a rename, the same way
// the alarm state is written. A write that fails is logged and the check
// continues: the next outage is then dated from an older reading, or from
// none, which alarms sooner rather than later.
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
		// The temporary is this invocation's own, so removing it is safe.
		_ = os.Remove(partial)
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(ctx, "backup.staleness.last_reading_write_failed",
			slog.String("path", path), slog.String("err", renameErr.Error()))
		return
	}
	logger.DebugContext(ctx, "backup.staleness.last_reading_written",
		slog.String("path", path), slog.Int("reading_count", len(record.Readings)))
}
