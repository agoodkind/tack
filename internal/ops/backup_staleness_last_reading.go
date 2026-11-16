// Local last-reading records preserve each mechanism's observed success and
// observation time during store or probe outages (TACK-515). Unreadable readings
// become stale only after age exceeds the threshold, unless this guest never
// observed success. The cache must remain local because shared alarm state
// depends on the unavailable object store.

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

// backupLastReadingFile identifies cached successes under the backup root.
const backupLastReadingFile = "staleness-last-reading.json"

// backupLastReading records success time and this guest's observation time.
type backupLastReading struct {
	SucceededAt time.Time `json:"succeeded_at"`
	ReadAt      time.Time `json:"read_at"`
}

// backupLastReadings indexes cached observations by metric name.
type backupLastReadings struct {
	Readings map[string]backupLastReading `json:"readings"`
}

// lastBackupStalenessSuccess returns a cached local success. Replication uses
// this observation rather than another guest's shared health marker.
func lastBackupStalenessSuccess(
	ctx context.Context,
	cfg *config.Config,
	name string,
) (time.Time, bool) {
	last, found := loadBackupLastReadings(ctx, cfg).Readings[name]
	if !found || last.SucceededAt.IsZero() {
		return time.Time{}, false
	}

	return last.SucceededAt.UTC(), true
}

// rememberBackupStalenessReadings records current successes, removes successes
// that the store explicitly denies, and dates unreadable metrics from cache.
// Already dated metrics retain the age that their probe calculated.
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

// rememberedBackupStalenessMetric calculates age from the cached success
// while preserving the unreadable cause and failure detail for reporting.
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
	telemetry.L(ctx).InfoContext(
		ctx,
		"backup.staleness.reading_remembered",
		slog.String("metric", metric.Name),
		slog.Time("succeeded_at", metric.At),
		slog.Time("read_at", metric.LastReadAt),
		slog.Bool("stale", metric.stale()),
	)

	return metric
}

// backupLastReadingPath returns the configured local cache path.
func backupLastReadingPath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupLastReadingFile)
}

// loadBackupLastReadings treats absent files as initial empty state. Read or
// decode failures log the cause and return empty state, leaving unreadable
// metrics stale rather than trusting a damaged historical record.
func loadBackupLastReadings(ctx context.Context, cfg *config.Config) backupLastReadings {
	empty := backupLastReadings{Readings: map[string]backupLastReading{}}
	path := backupLastReadingPath(cfg)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty
	}

	if err != nil {
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.last_reading_unreadable",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return empty
	}

	var record backupLastReadings
	if err := json.Unmarshal(body, &record); err != nil {
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.last_reading_unreadable",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return empty
	}

	if record.Readings == nil {
		record.Readings = map[string]backupLastReading{}
	}

	return record
}

// saveBackupLastReadings atomically replaces cached state through a rename.
// Failures log the cause without stopping the check. An older or absent cache
// can only trigger an earlier alarm during the next outage.
func saveBackupLastReadings(ctx context.Context, cfg *config.Config, record backupLastReadings) {
	logger := telemetry.L(ctx)
	path := backupLastReadingPath(cfg)
	body, err := json.Marshal(record)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.last_reading_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	if err := os.MkdirAll(cfg.BackupRoot, 0o750); err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.last_reading_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	partial, err := stageBackupAlarmState(ctx, path, body)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.last_reading_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	if err := os.Rename(partial, path); err != nil {
		// Only this invocation's temporary file requires cleanup.
		_ = os.Remove(partial)
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(
			ctx,
			"backup.staleness.last_reading_write_failed",
			slog.String("path", path),
			slog.String("err", renameErr.Error()),
		)

		return
	}

	logger.DebugContext(
		ctx,
		"backup.staleness.last_reading_written",
		slog.String("path", path),
		slog.Int("reading_count", len(record.Readings)),
	)
}
